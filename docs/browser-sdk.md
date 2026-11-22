# Browser SDK for published custom events

`packages/browser-sdk` provides the framework-neutral `@evia/browser-sdk` ES module. It sends existing registered custom events through the current public configuration and collection APIs. It has no runtime dependencies, framework integration, automatic DOM listeners for event discovery, or URL/query-string harvesting. The five reserved `sys.*` events are served through public configuration and reach queryable storage via the server-side embedded system catalog; identity graph processing remains separate work.

Create a browser source with the website's exact Origin, register the custom events and property schemas, publish and activate a ready release, then build the SDK locally:

```sh
pnpm --filter @evia/browser-sdk build
pnpm --filter @evia/browser-sdk test
```

Use the repository's configured pnpm version. The package is private workspace source and is not published to a registry. A local consumer can use the workspace package after its build, or create a local archive with `pnpm --filter @evia/browser-sdk pack --pack-destination /tmp/evia-sdk-package`. The archive contains the compiled ESM files and TypeScript declarations. Serve all imported module files when using native browser ESM.

```ts
import { createEviaClient } from '@evia/browser-sdk';

const evia = createEviaClient({
  endpoint: 'https://events.example',
  writeKey: 'your-public-browser-write-key',
  projectId: '00000000-0000-4000-8000-000000000001',
  environment: 'production',
});

await evia.init();
evia.navigate({ routeName: 'Item', routeTemplate: '/items/:id', navigationType: 'initial' });

// Both event keys and their properties must already be published for this source.
const page = evia.emit('item_page_opened', { item_id: 'example' });
evia.identify('application-user-id');
const action = evia.emit('item_saved', { item_id: 'example', desired_state: true });
await evia.flush();
const outcome = await action.result;
if (outcome.status === 'accepted') {
  // Durable queue acknowledgement only; storage/query visibility can follow later.
}

// At logout: future events receive a fresh anonymous/session/page context.
evia.reset();
// At application teardown: settle pending work, abort requests and remove listeners.
evia.destroy();
```

`emit` returns `{eventId, result}` immediately. It captures the event ID, UTC occurrence time, properties, UID/anonymous identity and page/session/tab context at that call. Property objects and arrays are copied, so later application mutations cannot change pending events. Configuration initialization and refresh are asynchronous and single-flight; emitting before initialization is supported within the same queue bounds. The returned outcome promise resolves for accepted, rejected, dropped and uncertain events. Synchronous input or backpressure errors throw `EviaSDKError` with a stable `code`, such as `QUEUE_FULL` or `EVENT_TOO_LARGE`.

An event absent from the source's current public configuration settles as `rejected` with `EVENT_NOT_PUBLISHED` and is not sent. The public `emit()` API accepts custom event keys only and cannot forge `sys.*` events, and the SDK never creates event definitions. The opt-in `autoPageViews` option is the sole producer of the reserved `sys.page_view` system event (see [Automatic page views](#automatic-page-views)). `init()` is recommended at application startup to check publication and source availability explicitly. Generic envelope/property bounds are checked locally; the server remains authoritative for the complete published JSON Schema, current source authorization and release eligibility. Property schema failures are returned through the per-event receipt.

## Identity and explicit navigation

`identify(uid)` sets only the raw UID on future events. By default it emits no event; with `autoIdentityLink: true` it additionally records one reserved `sys.identity_link` (see [Automatic identity links](#automatic-identity-links)). It never attaches previous anonymous events to an account. UIDs must contain 1–128 Unicode characters and no NUL. `reset()` removes the UID, rotates the anonymous ID and starts a fresh session/page context. Already queued events retain their earlier identities and contexts, including events whose acknowledgement is uncertain.

By default the anonymous UUID is kept in local storage under the project, environment and public write-key namespace. Source-key rotation starts a different namespace. Storage failures fall back to memory; `storage: null` makes the client memory-only. No cookies are read or written. Each client instance has a fresh tab/order UUID and monotonically increasing sequence. Reloading or creating another client starts a fresh tab/order scope, session and page context; only the anonymous UUID is persisted. Sessions are local to that instance and renew after 30 minutes of inactivity. These are raw browser identifiers, not resolved people. A navigation token joins pages within one live client, such as an SPA; hard navigation/reload does not retain that token or reconstruct a cross-document session/link.

The SDK never derives route names from the DOM or current URL. Call `navigate()` from application routing code with an explicit route template. It changes context and returns the new context. By default it emits no page event and the application must explicitly emit its registered page event; with `autoPageViews: true` it additionally records one reserved `sys.page_view` for the new page (see [Automatic page views](#automatic-page-views)). To link an activity on one page to the next page:

```ts
const link = evia.beginNavigation();
const source = evia.emit(
  'item_opened',
  { item_id: 'example' },
  {
    navigation: link,
    operationId: 'open-example',
  },
);
evia.navigate({
  routeName: 'Item',
  routeTemplate: '/items/:id',
  navigationType: 'push',
  link,
});
const destination = evia.emit('item_page_opened', { item_id: 'example' });
```

Both envelopes share the navigation UUID. The destination gets a new page UUID and the source/previous page UUIDs. A token from an earlier page, reset or expired session is rejected. Engagement, visibility and reading events also require explicit registered custom definitions and application calls; the SDK does not inspect content or invent business semantics.

## Automatic page views

Set `autoPageViews: true` at client construction to enable reserved automatic page collection. It is opt-in and defaults to `false`, so existing integrations keep identical behavior. When enabled the client enqueues exactly one `sys.page_view` event (empty properties) each time a fresh `page_view_id` is generated: the initial load, every successful `navigate()`, and `reset()`. The event reuses the normal bounded queue, retry and receipt pipeline and carries the new page/session context.

The reserved key is not reachable through the public `emit()` API, which still rejects `sys.*` with `INVALID_CUSTOM_EVENT_KEY`; automatic collection is the only internal producer. The event is subject to the same publication gate as custom events, so a source that has not published `sys.page_view` settles each automatic event as `rejected` with `EVENT_NOT_PUBLISHED` and never transmits it. Backpressure never breaks navigation: when the queue is full the automatic page event is dropped instead of throwing, while explicit `emit()` calls still surface `QUEUE_FULL`.

Automatic page views do not inspect the DOM or URL and do not infer routes; the application still drives every `navigate()` with an explicit route template. Scroll segments are emitted opt-in (see [Automatic scroll collection](#automatic-scroll-collection)). Session ends are emitted opt-in (see [Automatic session ends](#automatic-session-ends)). Operation failures are reported explicitly rather than auto-collected (see [Operation failure reporting](#operation-failure-reporting)). Cross-document session continuity is not reconstructed.

## Automatic identity links

Set `autoIdentityLink: true` to record a reserved `sys.identity_link` after every successful `identify(uid)`. It is opt-in and defaults to `false`, so existing integrations are unchanged. The UID is assigned before the event is enqueued, so its `identity` object carries both the new `uid` and the current `anonymous_id`; properties stay empty and no credentials, tokens or request bodies are collected. The event reuses the bounded queue, retry and receipt pipeline and the same publication gate: an unpublished `sys.identity_link` settles as `rejected` with `EVENT_NOT_PUBLISHED`, while the UID assignment performed by `identify()` still takes effect. A full queue drops the automatic event without throwing or reverting the UID. `reset()` rotates identity but never emits an identity link. As with page views, the reserved key cannot be forged through the public `emit()` API.

## Operation failure reporting

Call `reportOperationFailure(failure)` to record one reserved `sys.operation_failed` event for a terminal logical-operation failure. Unlike automatic collection this is an explicit application call and needs no opt-in flag, but it still passes through the publication gate: an unpublished `sys.operation_failed` settles as `rejected` with `EVENT_NOT_PUBLISHED`.

The input is a bounded structured object, not an exception: `operation` (1–128 characters), `errorCategory` restricted to `validation`, `network`, `timeout`, `http` or `business`, `retryable` boolean, and optional `errorCode` (1–128 characters). Unknown categories fail with `INVALID_ERROR_CATEGORY`, non-boolean `retryable` with `INVALID_RETRYABLE`. The event properties are rebuilt from these validated fields and mapped to snake_case; caller objects are never spread, so raw exceptions, request bodies, credentials and any unknown fields cannot leak into the event (the schema sets `additionalProperties=false`). The reserved key cannot be forged through the public `emit()` API, which still rejects `sys.*`.

## Automatic session ends

Set `autoSessionEnd: true` to record a reserved `sys.session_end` event with a closed `reason` before an observable session ends. It is opt-in and defaults to `false`, so existing integrations are unchanged. The reasons and their triggers are:

- `logout`: `reset()` clears identity; the event is enqueued before rotation so it keeps the ending session and the cleared uid in its context.
- `account_switch`: `identify(uid)` replaces an already-established different uid; the event is emitted before the uid changes, so it describes the prior account. Re-identifying the same uid and the first identify do not emit an event.
- `idle`: when inactivity reaches `sessionTimeoutMs`, the next activity rotates the session inside `touchSession`; activity is refreshed before the event is enqueued to prevent re-entrancy, and the event still carries the ending session id while the triggering event uses the new session.

Each event carries only the `reason` property and reuses the bounded queue, retry and receipt pipeline plus the publication gate: an unpublished `sys.session_end` settles as `rejected` with `EVENT_NOT_PUBLISHED`, and a full queue drops the automatic event without breaking `reset()`, `identify()` or the in-flight event. The reserved key cannot be forged through the public `emit()` API. The SDK does not reconstruct cross-document session continuity.

## Automatic scroll collection

Set `autoPageScrolls: true` to collect reserved `sys.page_scroll` segments. It is opt-in and defaults to `false`, so existing integrations are unchanged. The SDK subscribes through an injectable `ScrollHost`; by default it adapts `window`/`document` (and does nothing under SSR without those globals). Consecutive scroll activity is coalesced into one segment when scrolling has been idle for `scrollIdleMs` (default 1000, bounded 100–10000 ms), rather than emitting one event per scroll tick.

Each segment reports `from_ratio`, `to_ratio` and `max_ratio` clamped to `[0,1]`, a `direction` of `up`, `down` or `mixed`, and an integer non-negative `duration_ms`. Movement with no directional change produces no event. Production is bounded at twelve coalesced segments per rolling minute; segments beyond the cap are dropped rather than queued. When `navigate()` or `reset()` changes the page, an in-flight segment that has not yet stopped is discarded so it is never attributed to the next page view. Events reuse the bounded queue and publication gate (an unpublished `sys.page_scroll` settles as `EVENT_NOT_PUBLISHED`), and the reserved key cannot be forged through the public `emit()` API. The scroll listener is best-effort: a throwing `ScrollHost` is caught at the event boundary so it can never break the host page, and `destroy()` unsubscribes the host and ignores all later scroll activity.

## Automatic click heatmap collection

Set `autoHeatmapClicks: true` to collect reserved `sys.heatmap_click` events. It is opt-in and defaults to `false`. The SDK subscribes in the document capture phase through an injectable `HeatmapHost`; the browser adapter records each click's normalized `x_ratio`/`y_ratio`, bounded viewport dimensions and a structural `element_path`. Keyboard-generated activations use the target element's center when the click event has no pointer coordinates.

The structural path contains only lower-case tag names, a validated `role` and `nth-of-type` indexes, bounded to four ancestors and 128 characters. It deliberately excludes page text, input values, ids, classes, hrefs and arbitrary attributes. `element_index` is a bounded zero-based sibling index. Collection is capped at 120 clicks per rolling minute; malformed/out-of-range samples and excess clicks are dropped before queue allocation. Events reuse the existing bounded queue, stable IDs, retry receipts and publication gate; `destroy()` unsubscribes the listener. This is click-coordinate collection only: heatmap aggregation and console rendering remain separate work.

## Bounded delivery and receipts

The default queue holds at most 200 events and 256 KiB of reserved event payload. An event is limited to 8 KiB of UTF-8 envelope bytes, 50 properties, scalar values or arrays of at most 20 scalars, strings of at most 1024 Unicode characters, and finite numbers within JavaScript's safe numeric range. Nulls, nested objects, nested arrays, invalid Unicode and accessor properties are rejected. Queue accounting reserves room for the largest supported revision/version, so the local byte bound can conservatively reject an envelope slightly below the server limit.

Collection batches contain at most 20 events and 60 KiB by default, below the server's 50-event/256-KiB request limits. One collection request runs at a time. A flush sends at most four batches per turn, then schedules remaining work. A lifecycle flush sends at most one batch. Requests time out after five seconds; default retries use exponential delays from one second, at most three attempts and a five-minute total event age. All limits have validated finite configuration bounds. The maximum configurable age is 55 minutes, below the collector's one-hour occurrence-time window.

An uncertain retry preserves the complete serialized batch body, batch ID, send time and event envelopes. A valid partial receipt removes accepted and permanently rejected events; only retryable events remain, with the same event bytes, IDs, identities and release versions, in a new batch. `Retry-After` delays are honored when the header is visible to the browser. Older cross-origin deployments that do not expose the header use bounded local backoff. A delay beyond the event-age budget causes expiry rather than an early retry. No retry rewrites an event to a newer release.

The collector returns per-event receipts for HTTP 202, including mixed accepted/rejected results, and HTTP 422 when all items are permanently rejected. The SDK also handles a valid HTTP 207 partial receipt. Receipt batch IDs, complete unique indexes and event IDs are checked before applying any results. Malformed or missing acknowledgements are uncertain and may be retried with stable bytes. A 202 status alone is insufficient to settle an event as accepted.

| Outcome     | Meaning                                                                                                        |
| ----------- | -------------------------------------------------------------------------------------------------------------- |
| `accepted`  | The server's receipt acknowledges durable queue acceptance. It does not establish storage or query visibility. |
| `rejected`  | A definitive rejection with no prior ambiguous attempt; inspect `code`, `retryable` and optional `receiptId`.  |
| `dropped`   | No collection attempt was made: configuration failure, local lifecycle cancellation or age expiry.             |
| `uncertain` | An attempted event has no conclusive final acknowledgement. It may already have been accepted.                 |

`flush()` resolves with `{pending}` after its bounded turn, not after guaranteed delivery. Event outcome promises provide the individual results. New emissions may continue during a flush within the queue limits. The default memory queue does not survive process termination or reload; an opt-in durable mirror is described below. Delivery retries can produce duplicate receipts; the server deduplicates stable source/event IDs in its exact read view.

### Bounded bulk admission and outcome-scoped drain

High-volume producers that already hold encrypted content chunks can admit a non-empty list atomically with `emitEncryptedContentChunks(chunks)`. The SDK validates every chunk and reserves the complete list against the same configured queue event/byte limits before appending anything. If the list is malformed, larger than `maxQueueEvents`, or does not fit the remaining queue budget, the call throws without enqueuing a hidden prefix. It returns one `Emission` per input in the same order and does not call `flush()` per item. Individual events still use the existing batch event/byte limits, stable IDs, immutable configuration revision, telemetry token, retry and persistence path.

Use `drainUntilSettled(emissions, options)` with non-duplicate handles created by the same client to drive repeated normal flush turns beyond the four-batch `flush()` limit while observing only those handles:

```ts
const emissions = evia.emitEncryptedContentChunks(encryptedChunks);
const delivery = await evia.drainUntilSettled(emissions, {
  timeoutMs: 5 * 60_000,
  maxRounds: 1024,
  signal: abortController.signal,
});

if (delivery.status !== 'accepted') {
  // Inspect delivery.outcomes and delivery.pendingEventIds; do not advance the
  // producer checkpoint. Queued events remain eligible for normal delivery.
}
```

`status: "accepted"` is returned only when every provided handle has settled with an `accepted` receipt. A provided `rejected`, `dropped` or `uncertain` outcome, or any still-pending handle, always produces `status: "incomplete"`. `reason` is `settled`, `deadline`, `max_rounds` or `aborted`; `settled` only means every handle completed and is not itself a success signal. `outcomes` contains settled provided handles in input order, while `pendingEventIds` contains only unsettled provided IDs—unrelated queue entries never affect or appear in the result.

The default timeout is the client's `maxAgeMs` (at most 55 minutes). `maxRounds` defaults to 1024 and is bounded to 1–4096; each round retains the ordinary maximum of four collection batches. Retry delays, including visible `Retry-After` on 429, wait without consuming rounds. Concurrent drain callers share the existing collection single flight. Aborting or timing out cancels only that caller's wait: it does not abort a shared request, delete queue entries or reinterpret pending work as delivered.

With `persistQueue`, bulk entries are mirrored in one queue snapshot and retain event IDs, attempt counts and ambiguity across reload. The originating page's JavaScript promises still cannot survive reload, so a new page can redeliver rehydrated entries but cannot recreate the old `Emission[]` or claim their outcome-scoped success; producer checkpoints must remain outside that lost page or be reconciled from downstream durable state.

## Durable queue persistence (opt-in)

Set `persistQueue: true` to write-through the bounded event queue onto the injected storage port (the browser `localStorage` adapter by default, injectable for tests and absent under SSR) under a dedicated `...:queue` key separate from the anonymous identifier. The existing event/byte bounds still apply and every storage failure fails soft back to memory, so collection is never broken by an unavailable store.

- Snapshot format `evia.queue.v1` stores only serializable entry fields; the per-emission outcome promise belongs to the originating page and is not persisted.
- On construction the client rehydrates the snapshot, prunes expired (`maxAgeMs`), malformed and over-budget entries, and preserves every original `event_id`, then writes back the pruned snapshot.
- Rehydrated entries without a ready envelope are re-resolved against the current public configuration; events no longer published fail closed with `EVENT_NOT_PUBLISHED`.
- Delivery stays at-least-once: a redelivered event keeps its stable `event_id`, and the collector deduplicates stable source/event IDs, so reload redelivery cannot create duplicate facts.
- The original emitter's outcome promise does not survive reload; post-reload results are observable through delivery and receipts rather than the pre-reload `Emission.result`.
- v1 scopes durability to reload and same-context navigation while storage is available. Cross-tab writer coordination is not implemented; concurrent tabs rely on the same idempotent redelivery, and browser-driven storage eviction stays outside SDK control. Omitting the option keeps the historical memory-only behavior byte-identical.

## Cross-document session continuity (opt-in)

Set `persistSession: true` to continue the session scope across a hard reload or cross-document navigation inside the same tab. The mutable scope — `session_id`, `tab_id`, `sequence` and the last activity timestamp — is mirrored write-through onto a tab-scoped storage port (the browser `sessionStorage` adapter by default, injectable via `sessionStorage`) under a dedicated `...:session` key, separate from the anonymous identifier and the durable queue. Omitting the option keeps the historical per-document scope byte-identical: every document still starts a fresh session, tab and sequence.

- Snapshot format `evia.session.v1` restores the scope verbatim on construction. The idle-timeout rotation stays lazy in `touchSession()`, so the first activity after a long absence observes the same `sys.session_end` (`idle`) emission and rotation as the live in-memory path; construction itself never emits.
- The sequence stays strictly monotonic across the document boundary and is never reset — not by reload, idle rotation or `reset()` — until it exhausts the safe integer range, which keeps failing closed with `SEQUENCE_EXHAUSTED`.
- Page scope is deliberately not persisted: a reload is a new document and produces a new `page_view_id` (and, with `autoPageViews`, a new reserved page-view event) inside the continued session.
- `reset()` rotates the session and persists the rotated scope, so the next document in the tab starts fresh rather than rejoining the discarded session.
- The port must be tab-scoped. Two live tabs sharing one session would emit colliding sequences into a single session and break journey ordering, so the default is `sessionStorage`; a new tab, including a duplicated tab whose storage the browser copied, starts its own scope. Duplicated-tab copies are a documented boundary the same way the shared anonymous identifier is.
- Enabling `persistSession` without a usable port (for example `sessionStorage: null` under SSR) fails closed at construction with `SESSION_STORAGE_UNAVAILABLE` instead of silently losing continuity; runtime storage errors fail soft to memory for the writes, exactly like the durable queue.
- Rehydrated durable-queue events keep their original session context: the queue snapshot is immutable history, while new events from the reloaded document continue the persisted scope with strictly greater sequences.

## Configuration and lifecycle

Public configuration supplies project/environment, release revision and source-allowed event versions/schemas. It may additionally advertise `content_encryption` (`key_id`, `alg=RSA-OAEP-256`, browser-safe public JWK); the SDK rejects malformed keys and any JWK containing a private `d` parameter. The key participates in the config ETag so rotation invalidates cached configuration. The config supplies no expiry timestamp, lease or compatibility deadline. The client pins the expected project/environment, uses a conservative 30-second local TTL, and revalidates expired configuration through an authorized GET with the current revision and ETag when available. A 304 renews the local cache only after the server's checks. Refresh failure blocks new envelopes; the server still checks every collection attempt. `refreshConfig()` forces revalidation. Compatible revision updates affect future envelopes only.

Fetch uses `credentials: "omit"`, `cache: "no-store"`, `redirect: "error"` and `referrerPolicy: "no-referrer"`. Browser source secrets and platform cookies are never accepted as SDK options. Configuration is limited to 1 MiB and nesting depth 64; receipts are limited to 64 KiB. An oversized configuration fails closed instead of allocating unbounded client state. The SDK does not implement the entire JSON Schema vocabulary locally.

Every bounded collection fetch is keepalive-capable from submission, so an ordinary request already in flight can be reused during a lifecycle transition. By default `visibilitychange` to hidden and `pagehide` reuse that single flight or attempt one bounded keepalive batch. Keepalive is best effort: the browser may terminate it, restrict aggregate keepalive bytes or stop JavaScript before a receipt arrives. No sendBeacon result is used as proof of delivery. No blocking XHR or unload wait is used. `lifecycle: false` disables these listeners.

`disable()` stops emission, clears timers, aborts requests and settles unsent items as dropped and attempted items conservatively as uncertain. An already submitted request may still have been accepted remotely. `enable()` revalidates configuration. `destroy()` additionally removes lifecycle listeners and permanently closes the instance. Generation checks prevent late responses from repopulating queues or replacing newer configuration after disable/enable. Scope options are immutable; destroy the old client before creating one for another project, environment, endpoint or source.

## Verification boundary

Package tests cover asynchronous retries, partial/all-rejected receipts, timeout, malformed acknowledgements, backpressure, identity snapshots, explicit navigation, configuration expiry/ETags/scope, lifecycle cleanup and byte/age bounds. Run `pnpm --filter @evia/browser-sdk measure` after building for local module sizes and five bounded 200-event synchronous bursts, including event-loop delay and teardown. This Node measurement does not establish browser, network or million-user capacity. Real browser/CORS and complete API/pipeline evidence must be recorded separately.

On 2026-11-09, the package build and 20 unit tests passed with Node 24.22.1. The measured ESM JavaScript occupied approximately 33.7 kB uncompressed and 8.8 kB when each module was gzip-compressed separately. A local npm pack dry run reported an approximately 10.0 kB archive and 40.3 kB unpacked, including declarations and package metadata. Five bursts of 200 emissions took 1.55–3.39 ms of synchronous emission work; observed event-loop delay including teardown was 1.86–7.92 ms. The 201st queued event rejected immediately at the default bound in every sample. These are local Node observations with a mock configuration fetch and no collection network work; rerun the measurement for the current build and deployment environment.

Real cross-origin browser configuration, response-loss replay, five deduplicated stored events, raw-identity/navigation queries and source revocation passed in the [dashboard and SDK verification](verification/dashboards-sdk-2026-11-09.md). This is functional evidence, separate from the bounded Node measurements above.

On 2026-11-20, opt-in session continuity passed 10 additional unit tests (73 total) and a dedicated full-stack browser verification: a synthetic site reloaded inside one tab continued the same `session_id` and `tab_id` with a strictly increasing sequence, a second tab started an isolated session, and a warehouse `events` query filtered by the continued session returned both documents' events; see [session continuity verification](verification/sdk-session-continuity-2026-11-20.md).
