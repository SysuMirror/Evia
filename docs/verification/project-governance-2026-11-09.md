# Project governance backend verification

Verified on 2026-11-09 (Asia/Shanghai). This batch delivers seven session-authenticated governance endpoints and an operator account-provisioning command. It adds backend capabilities; it does not add console pages or establish production capacity.

## Implementation and contracts

The authoritative contract adds owner member administration, human member settings reads, owner settings updates and owner audit queries. Governance writes require CSRF, revision and idempotency headers. Machine credentials cannot administer these resources. Generated Go/offline validators and TypeScript types were synchronized (123 wire types); contract verification, naming checks, generation drift and TypeScript checking passed.

Migration `011_project_governance.sql` adds independent membership/settings revisions, shared durable mutation snapshots and separate operator provisioning audit records. No event facts or immutable publications are rewritten. New modules remain in the access domain with narrow store/service ports; HTTP parsing, persistence and operator command handling are separate.

## Categorized backend evidence

Full `make backend-test` passed: formatting, `go vet ./...` and `go test -race ./...`. The complete real PostgreSQL integration race package passed in **101.210 seconds**, using isolated schemas. These durations are test times, not API latency.

Focused evidence was collected before the full regression:

| Area | Actual evidence |
| --- | --- |
| Memberships | Unit race tests passed in 1.019 s; eight top-level real PostgreSQL race tests passed in 4.339 s. |
| Project settings | Unit race tests passed in 1.057 s; three real PostgreSQL race tests passed in 1.763 s. |
| Audit queries | Unit race tests passed in 1.020 s; four real PostgreSQL race tests passed in 2.751 s. |
| Account provisioning | Scoped domain/CLI race checks passed in 13.459/15.872 s; real PostgreSQL provisioning race tests passed in 33.265 s. After a prerequisite error-wording correction, affected CLI and database checks passed again. |
| Governance HTTP/runtime | Contract-backed focused HTTP tests passed in 4.806 s; scoped control/command race checks passed in 81.425/14.174 s. |

Membership tests cover enabled-owner preservation during opposite owner mutations, current authorization before replay, delete/re-add revision protection, 1,000-member and safe-integer limits, exact concurrent retries and audit rollback. A deterministic PostgreSQL lock-wait test proves a target's existing catalog transaction can acquire its project lock and finish before membership mutation continues. Actual query-store tests exercise queued/running raw-query demotion and running aggregate-query membership removal.

Settings tests cover owner/editor/viewer/outsider/machine boundaries, disabled accounts, eight-way revision competition, eight-way identical retry, original response snapshots after later updates, and audit rollback. A blocked membership lock followed by committed revocation verifies that old idempotency keys do not preserve former owner authority.

Audit tests cover timestamp ties, descending ID continuation, exact actor/action filters, cross-project denial, current role/account changes, nanosecond half-open filter boundaries over PostgreSQL microsecond timestamps, and exact machine credential attribution without credential contents. Reads produce no new audit entries; authority locks stay held while the actual database query is blocked.

Provisioning tests verify that bootstrap is still required, disabled-only installations cannot create accounts through this path, duplicate usernames never replace credentials, concurrent duplicates create exactly one account, and account/principal/operator-audit writes roll back together. CLI tests ensure credentials and raw driver diagnostics are not printed.

HTTP tests use the real contract validator and cover every route's authentication, CSRF, strict input, revision/idempotency requirements, resource IDs, ETags, no-store responses, cursor reader/project/filter binding, safe errors and required nullable metadata. Independent read-only review found no remaining blockers; it requested consistent rejection of even an empty attached machine identity, which was implemented and tested.

## Real operator and HTTP workflow

The functional environment explicitly migrated to `011`. API, ingestion and query workers use `/tmp/evia-governance-runtime`, built from backend commit `82dbf61`; backend tree `a488a89d9b73a6e7e254d0d0d12a9340eea8205d`, binary SHA-256 `d84dead0650cd8179e75c67b6b81d0403330d156d33e1d95ac76e53aa805eff2`. Readiness returned HTTP 204. The separate million-event capacity runtime was not modified.

The reproducible `tooling/verification/governance_smoke.py` passed against that runtime. It uses the actual `account-create` command, three independent synthetic accounts and **44 actual HTTP requests**. It does not alter SQL membership, revision or audit records directly.

- Creates a project, rejects removal of its sole enabled owner, adds a member and verifies exact accepted-response replay.
- Reads two distinct member pages, changes the roster and rejects its old cursor.
- Verifies an editor can read settings but cannot change them or list members. Owner updates become visible in the existing project API; exact retry produces the same response and one settings audit entry, while a fresh stale write fails.
- Demotes an editor and verifies the existing event-definition API now denies writes; removing the member denies subsequent project/settings reads.
- Reads real audit pages, proves non-overlap and exact filtering, rejects cursor reuse with changed filters and checks the response contains only the documented safe metadata.
- Creates a valid machine credential, proves it works through machine identity discovery, and proves it cannot access members, settings or audit endpoints.
- Self-demotes the original owner after adding another owner. Old membership/settings retries and audit reads now fail. The remaining owner cannot remove itself and can revoke the test machine principal. All three human sessions are logged out.

The smoke tool provisions its own primary account rather than reusing the browser-test account, keeping verification independent of that account's login-rate budget. Credential values and captured operator diagnostics are not emitted. It has a loopback-only target, bounded requests/responses and an overall workflow budget. Synthetic accounts/projects remain in the dedicated test environment.

The workflow has been added to hosted CI before the existing browser regression. Hosted outcomes are recorded in [GitHub Actions](https://github.com/howtomakeaname/Evia/actions/workflows/verify.yaml); local HTTP success does not substitute for the hosted result.

## Verification setup and limits

Two local verification setup mistakes were corrected before successful runs: formatting was initially launched while contract regeneration was rewriting its output, and a private environment loader's tuple was initially treated as a dictionary. The successful checks ran after generation completed and used the existing private-environment runner. Neither interrupted command produced passing backend evidence.

No new console UI, frontend screenshots, large-data benchmark or capacity claim is included. The existing browser suite is retained in CI to detect regression in previous pages. Governance transaction/query tests do not prove production multi-replica recovery or bounded latency over a large audit history. Account recovery/disablement, invitation/registration, quotas, lifecycle/export and dedicated governance pages remain open; the [million-event query failures](query-capacity-2026-11-09.md) also remain unresolved by this batch.
