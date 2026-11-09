# Machine access and public integration verification

Verified on 2026-11-09 (Asia/Shanghai). This delivery adds independent machine principals, explicitly scoped project APIs, owner administration and public business integration assets. It does not complete the remaining [product scope](../feature-coverage.md) or replace the failed cases in the [million-event query report](query-capacity-2026-11-09.md).

## Backend and migration

The functional environment explicitly applied PostgreSQL migration `010_machine_principals.sql`. Existing human account identifiers are preserved in a shared principal registry; membership and browser sessions remain human-only. Eleven attribution foreign keys now reference principals. Machine credentials are stored as hashes, with immutable project/environment grants, expiry, bounded rotation overlap and permanent revocation. Audit records identify the exact credential.

- Full `go vet ./...` and `go test -race ./...` passed. The complete real PostgreSQL integration race package passed in **81.101 seconds**. After idempotency-key/canonical-replay alignment, focused real machine persistence tests passed in **9.711 seconds**.
- Five real catalog/source scope tests passed in **2.323 seconds**; existing catalog/source/delivery regressions passed in **12.120 seconds**. Query/reporting authorization regressions passed in **26.945 seconds**, with additional CRUD and privacy cases passing separately.
- Tests exercise cross-project/environment denial, independent operation grants, authorization before idempotent replay, concurrent lifecycle transactions, revision conflicts, atomic dashboard admission, historical migrations and credential attribution. Workers recheck the original credential at claim, lease checks and completion; a replacement credential cannot revive revoked unfinished work.
- HTTP tests cover strict bearer/session separation, duplicate and malformed authentication, CSRF for session mutations, owner-only administration, one-time secret recovery and the full 128-credential response boundary. Contracts and Go/TypeScript generation passed.

The actual API, ingestion and query processes used `/tmp/evia-machine-runtime`, SHA-256 `83847935f2e78ba477d147868389a1dbc0d1618a375e6ae08c006d7eb536dd07`. Its backend source tree is `cde823537d1ef8e1a38318636e90afb71b94a915`, unchanged by later console, documentation and unpublished commit-order work. API readiness returned HTTP 204. This is the isolated synthetic functional environment, separate from the million-event dataset.

Completed project results remain readable by another currently authorized caller, subject to environment and raw-data permissions. Revoking their original credential prevents further work under that credential; it does not erase shared project results. Project-wide draft permissions and the distinct release environment are explicit. These policies are documented in [machine access](../machine-access.md).

## Public integration assets

The three public `activity-*` skills cover event planning, publication and SDK integration verification. Desired event manifests are separate from published schemas and immutable release identifiers. The API helper checks machine identity, bounds requests/responses, refuses redirects and ambient cookies, and accepts credentials only through a private file or environment. Output files are created exclusively with mode 0600. A real POSIX 20-second per-call deadline covers headers and streaming bodies as well as connection setup.

All **13 asset tests passed**, including raw malformed HTTP responses, slow headers/body, private output, strict JSON and archive boundaries. Review found and corrected a missing discovery permission in the publication instructions and potential exception-text disclosure from malformed HTTP status lines. The packaging allowlist contains exactly nine public files, excluding internal instructions, credentials and symlinks.

The verified nine-file ZIP has SHA-256 `b5017dbe1a60dde04d837e24be92b766ea3e6aa76ac57427284b34baca02677e`. ZIP packaging is not an SDK npm release. The helper is a bounded request tool, not a complete automatic publication CLI; the skills still require a coding agent to reason about business events and application code.

`tests/browser/machine-integration-workflow.ts` uses the actual Python helper and compiled browser SDK:

1. A human creates a dedicated synthetic project and grants an independent machine principal. Thereafter the helper discovers identity, validates a complete desired event manifest, creates drafts, publishes, waits for real delivery readiness, activates and creates a browser source.
2. An independent loopback website emits a real SDK event across origins without platform cookies or Authorization. Real PostgreSQL jobs and the ClickHouse worker verify the stored source, event, UID and context.
3. The owner revokes the exact credential. Its next helper request receives actual HTTP 401 and creates no successful output file.

The focused flow passed in **13.8 seconds**, then ran again inside the final full suite. The first focused attempt exposed test instrumentation that omitted the browser Origin header; reading complete request headers fixed the observation. Readiness, collection receipts, persisted facts and authorization responses were never fabricated. These are synthetic integration tests, not acceptance in a user's actual website repository.

## Owner console and browser verification

The console uses the shared native design system, a lazy machine workspace, grouped explicit permissions and a wide detail dialog. Typed administration methods remain session/CSRF based. Secrets are held only in local issuance state, outside query caches. Changing the session, including a new login by the same user, destroys this state.

The real browser workflow verifies:

- Creation and rotation where the server commits successfully but response delivery is interrupted. Exact retries return original metadata without re-exposing a secret; a replacement requires explicit new rotation.
- Unicode name limits, invalid overlap focus, dirty navigation protection, expiry, credential/principal revocation and real remote revision conflicts. Refresh restores the confirmation input only while it still owns focus.
- An actual same-user login rotates the browser session; a subsequent session refresh clears the old secret and pending state. No mocked session response substitutes for this check.
- Desktop credential columns and actions remain visible. Mobile facts wrap, dialog sections stay inside their width, and wide tables scroll within their own region. Automatic traces, videos and screenshots are disabled for credential flows; explicit captures contain metadata only.

The development baseline passed behavior checks but visual inspection exposed detail-dialog clipping. A shared wide size and constrained grid sections corrected it. The first focused production run then failed waiting for a session refresh because the test dispatched a non-bubbling visibility event at the document; the application's listener is on the window. Correcting the test event to bubble made it exercise the existing listener. The production application was not rebuilt for that test-only change.

The final focused production chain passed in **53.9 seconds**. The complete suite then passed **45 / 45 in 1.4 minutes**, one worker, zero retries, with entry `/assets/index-CNiZv9Q6.js`. Root and browser reviewers inspected the final [desktop](../assets/machine-access-desktop.png) and [mobile](../assets/machine-access-mobile.png) screenshots. They are unedited captures of synthetic administration data, not generated UI. Browser timestamps use the test browser's timezone.

The API client passed **16 tests**; the existing SDK passed **20 tests**. `make web-check boundaries-check integration-assets-test` passed. The dependency check found no cycles across 115 scoped modules and 286 imports. Hosted CI runs separately in [GitHub Actions](https://github.com/howtomakeaname/Evia/actions/workflows/verify.yaml); the production browser evidence above does not stand in for a hosted run.

## Transfer size and limits

Same local toolchain and actual frozen assets, using Python `gzip.compress(..., mtime=0)` with default compression. Values are bytes, not latency measurements.

| Asset | Prior dashboard build, raw / gzip | Machine console, raw / gzip |
| --- | ---: | ---: |
| Initial JavaScript | 377,403 / 115,961 | 378,796 / 116,332 |
| Shared JSX chunk | 8,911 / 3,401 | unchanged |
| Initial CSS | 32,161 / 6,817 | 32,208 / 6,827 |
| Lazy machine JavaScript | absent | 20,894 / 7,379 |
| Lazy machine CSS | absent | 1,498 / 532 |

The existing lazy chart-size warning remains. No new ingestion/query capacity measurement is claimed. Machines currently have immutable grants in one project and environment, with lifetime limits of 256 principals per project and 128 credentials per principal. Bulk cross-environment identities, mutable permissions, automated publication CLI, actual business-repository acceptance and SDK registry distribution remain separate work. Identity resolution, independent metrics, members, data lifecycle, export and advanced analyses remain open.
