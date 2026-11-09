# Dashboard and browser SDK verification

This delivery restores product feature work alongside capacity engineering. It adds a framework-neutral browser SDK and versioned dashboard backend; console verification is recorded separately below. Missing product capabilities remain explicit in the [feature coverage register](../feature-coverage.md).

## Backend and persistence

Backend commits `97092ab`, `189c511`, `0945924` and `6e200ab` add safe collection response-header exposure and the dashboard domain, contract, PostgreSQL storage and HTTP/runtime wiring.

- Full `go vet ./...` and `go test -race ./...` passed.
- Ten dashboard PostgreSQL integration race tests passed in 6.941 seconds. They cover publication and revision conflicts, concurrent idempotent runs, cross-project/environment/role checks, pinned dependencies, all-widget admission rollback, audit rollback, cancellation/expiry, query cleanup references and exact nanosecond execution inputs.
- The complete PostgreSQL integration race package passed in 53.067 seconds after the new migration. Tests use isolated schemas against actual PostgreSQL, not mocked SQL.
- Contract validation and Go/TypeScript generation passed. Thirty unimplemented non-dashboard OpenAPI operations and three superseded dashboard placeholders were removed; their requirements remain in the feature register.

The functional runtime explicitly applied migration `009_dashboards.sql` using `evia migrate`. The API, ingest and query processes use `/tmp/evia-dashboard-sdk-runtime`, built from `6e200ab`, SHA-256 `702fef3fd2304ba9985440ebd48e4e5a524556af677bc92323d3d254c4afad17`. API readiness returned 204. This is the synthetic local functional environment, separate from the million-event capacity environment.

## SDK and engineering boundaries

SDK commit `cb998e5` provides a private workspace ESM package with zero runtime dependencies. Twenty asynchronous tests and the package build passed. Tests cover response loss, partial/all-rejected receipts, exact byte replay, TTL/ETag scope, timeout and abort generations, byte/count/age limits, raw identities and explicit navigation, lifecycle keepalive and cleanup.

The HTTP collector now exposes `Retry-After` and `X-Request-ID` only on authorized cross-origin responses, without allowing credentials. The new actual-collector tests failed against the prior implementation, then passed with the header correction for 429 and 503 responses.

Eighteen package-boundary tests pass, including rejection of React, management-client and design-system dependencies from the browser SDK. SDK test/build commands are included in `make check`. Local package size and bounded Node emission measurements, including their limitations, are recorded in [SDK documentation](../browser-sdk.md).

## Real browser SDK integration

The focused real-console browser flow passed in **29.4 seconds**, one worker, zero retries, against the updated backend and the prior frozen console build. It is SDK/API evidence, not evidence for the new dashboard UI.

`tests/browser/browser-sdk-workflow.ts` starts a separate loopback website serving only the compiled SDK ESM modules. A clean browser context uses the actual cross-origin public configuration and collection endpoints. No success responses, stored facts or readiness flags are fabricated.

- Five custom events capture anonymous activity, identified navigation source/destination, and post-reset anonymous activity. No platform cookies or Authorization header accompany collection.
- The first POST reaches the actual backend and receives 202 before the test interrupts response delivery. The SDK replays identical serialized bytes; all five outcomes eventually report accepted on attempt two.
- Real query jobs observe exactly five distinct stored events. Raw identity, session, navigation/page links and sequence values match the emitted snapshots. UID and original anonymous journeys contain exactly two and three events respectively.
- Source disablement returns actual HTTP 403 `SOURCE_DISABLED`. Browser configuration refresh then fails closed and no additional collection batch is emitted. CORS may hide the revoked source's error body from browser JavaScript; this is checked independently from the server status.
- Both the console and isolated SDK page have zero uncaught page errors. Credential responses are excluded from automatic traces, videos and screenshots.

## Dashboard console

The console now implements dashboard creation, published-analysis selection, keyboard layout editing, validation/difference acknowledgement, immutable publication, version history, runtime filters and independent child results. Four model tests cover Unicode limits, clear placement, reading order and monotonic status after delayed responses. Thirteen API-client tests pass. The scoped module graph has no detected runtime cycles.

The first complete 42-case browser attempt passed 41 cases and exposed a test-role mismatch: warning feedback correctly used `status`, while the test expected `alert`. The locator was corrected. A later visual pass caught excessive nested padding and a wide table stretching the chart; scoped layout fixes isolate table scrolling and constrain chart width. A 374.39-pixel first-card position failed the new 360-pixel desktop budget; spacing was reduced without reducing control heights.

The final focused console chain passed in **42.4 seconds** against production entry `/assets/index-C_5Zmr32.js`, including both dashboard and SDK helpers:

- Two distinct published analyses are selected through the UI and produce real charts and complete tables with count **3** and raw-UID distinct count **2**.
- Lost publication and execution responses replay the same request body/key; no duplicate version or run is created.
- A typed published-property filter empties one widget while its sibling retains data. A direct filtered v2-to-v1 switch clears both the editable filter counter and dialog while keeping the old result snapshot identified.
- A real remote draft update causes a revision conflict; local input is preserved and can be rebased. Keyboard resize rejects collision, restores focus and keeps widget identity. Unsaved navigation remains recoverable.
- Desktop first card begins within 360 pixels; at 390 × 844, it begins within 650 pixels. Every chart fits its card width/right edge and the page has no horizontal overflow. Tables retain their own horizontal scrolling. Zero uncaught page errors are asserted.

Root and the browser agent inspected the fresh [desktop](../assets/dashboard-desktop.png) and [mobile](../assets/dashboard-mobile.png) captures. They are unedited screenshots of synthetic facts, not generated UI or production customer data.

The final complete browser suite passed **42 / 42 in 1.2 minutes**, one worker, zero retries, on the same `/assets/index-C_5Zmr32.js` build. This includes the previous console/component cases, the four dashboard model cases, the combined real dashboard/SDK workflow and credential lifecycle.

## Production asset sizes

Same local toolchain, actual frozen assets; gzip uses Python `gzip.compress(..., mtime=0)` at its default level. These measurements establish transfer size, not runtime latency or production capacity.

| Asset                     |   Prior saved-analysis build | Dashboard delivery |
| ------------------------- | ---------------------------: | -----------------: |
| Initial JavaScript        | 374,110 bytes / 115,268 gzip |  377,403 / 115,961 |
| Initial CSS               |               30,605 / 6,583 |     32,161 / 6,817 |
| Lazy dashboard JavaScript |                       absent |    47,759 / 14,638 |
| Lazy dashboard CSS        |                       absent |        3,446 / 940 |
| Lazy chart JavaScript     |            550,372 / 185,018 |          unchanged |

The existing chart-size warning remains visible. The dashboard and charts load on demand; the browser SDK is a separate package and does not become a console runtime dependency.

## Limits

Dashboard admission is transactional, but child queries execute independently and do not share an atomic data snapshot. Existing project budgets remain unchanged. Each widget's query controls its own result expiry; an expired child does not invalidate other readable results.

The SDK is available as source and local packaging, not an npm release. Context continuity currently covers one live client instance and explicit SPA routing; hard-navigation session continuity, automatic system-event registration and resolved identity graphs remain separate work. This delivery introduces no new large-scale capacity claim and does not replace the failed cases in the [million-query report](query-capacity-2026-11-09.md).
