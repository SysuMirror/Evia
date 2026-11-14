# Browser SDK for published custom events

`packages/browser-sdk` provides the framework-neutral `@evia/browser-sdk` ES module. It sends existing registered custom events through the current public configuration and collection APIs. It has no runtime dependencies, framework integration, automatic DOM listeners for event discovery, or URL/query-string harvesting. System-event registration and identity graph processing remain separate work.

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

`identify(uid)` sets only the raw UID on future events. It emits no identity-link event and does not attach previous anonymous events to an account. UIDs must contain 1–128 Unicode characters and no NUL. `reset()` removes the UID, rotates the anonymous ID and starts a fresh session/page context. Already queued events retain their earlier identities and contexts, including events whose acknowledgement is uncertain.

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

Automatic page views do not inspect the DOM or URL and do not infer routes; the application still drives every `navigate()` with an explicit route template. Scroll, engagement, identity-link and session-end system events remain contract samples only and are not auto-collected. Cross-document session continuity is not reconstructed.

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

`flush()` resolves with `{pending}` after its bounded turn, not after guaranteed delivery. Event outcome promises provide the individual results. New emissions may continue during a flush within the queue limits. Memory queues are not a durable offline store and do not survive process termination or reload. Delivery retries can produce duplicate receipts; the server deduplicates stable source/event IDs in its exact read view.

## Configuration and lifecycle

Public configuration supplies project/environment, release revision and source-allowed event versions/schemas. It supplies no expiry timestamp, lease or compatibility deadline. The client pins the expected project/environment, uses a conservative 30-second local TTL, and revalidates expired configuration through an authorized GET with the current revision and ETag when available. A 304 renews the local cache only after the server's checks. Refresh failure blocks new envelopes; the server still checks every collection attempt. `refreshConfig()` forces revalidation. Compatible revision updates affect future envelopes only.

Fetch uses `credentials: "omit"`, `cache: "no-store"`, `redirect: "error"` and `referrerPolicy: "no-referrer"`. Browser source secrets and platform cookies are never accepted as SDK options. Configuration is limited to 1 MiB and nesting depth 64; receipts are limited to 64 KiB. An oversized configuration fails closed instead of allocating unbounded client state. The SDK does not implement the entire JSON Schema vocabulary locally.

Every bounded collection fetch is keepalive-capable from submission, so an ordinary request already in flight can be reused during a lifecycle transition. By default `visibilitychange` to hidden and `pagehide` reuse that single flight or attempt one bounded keepalive batch. Keepalive is best effort: the browser may terminate it, restrict aggregate keepalive bytes or stop JavaScript before a receipt arrives. No sendBeacon result is used as proof of delivery. No blocking XHR or unload wait is used. `lifecycle: false` disables these listeners.

`disable()` stops emission, clears timers, aborts requests and settles unsent items as dropped and attempted items conservatively as uncertain. An already submitted request may still have been accepted remotely. `enable()` revalidates configuration. `destroy()` additionally removes lifecycle listeners and permanently closes the instance. Generation checks prevent late responses from repopulating queues or replacing newer configuration after disable/enable. Scope options are immutable; destroy the old client before creating one for another project, environment, endpoint or source.

## Verification boundary

Package tests cover asynchronous retries, partial/all-rejected receipts, timeout, malformed acknowledgements, backpressure, identity snapshots, explicit navigation, configuration expiry/ETags/scope, lifecycle cleanup and byte/age bounds. Run `pnpm --filter @evia/browser-sdk measure` after building for local module sizes and five bounded 200-event synchronous bursts, including event-loop delay and teardown. This Node measurement does not establish browser, network or million-user capacity. Real browser/CORS and complete API/pipeline evidence must be recorded separately.

On 2026-11-09, the package build and 20 unit tests passed with Node 24.22.1. The measured ESM JavaScript occupied approximately 33.7 kB uncompressed and 8.8 kB when each module was gzip-compressed separately. A local npm pack dry run reported an approximately 10.0 kB archive and 40.3 kB unpacked, including declarations and package metadata. Five bursts of 200 emissions took 1.55–3.39 ms of synchronous emission work; observed event-loop delay including teardown was 1.86–7.92 ms. The 201st queued event rejected immediately at the default bound in every sample. These are local Node observations with a mock configuration fetch and no collection network work; rerun the measurement for the current build and deployment environment.

Real cross-origin browser configuration, response-loss replay, five deduplicated stored events, raw-identity/navigation queries and source revocation passed in the [dashboard and SDK verification](verification/dashboards-sdk-2026-11-09.md). This is functional evidence, separate from the bounded Node measurements above.
