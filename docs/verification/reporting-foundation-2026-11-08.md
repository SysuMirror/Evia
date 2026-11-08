# Reporting foundation verification — 2026-11-08

Scope: exact query `event_versions`, analysis capabilities, published catalog projection, read-only query validation, authoritative contracts/generated types, and runtime route wiring. This is backend foundation evidence, not acceptance of saved analyses, dashboards, machine principals or production capacity. The implementation was checked on the worktree based on `77fd07b`; it was not deployed to the existing running API.

## Recorded checks

The implementation agents reported successful completion of the following commands from `backend/`:

```sh
go vet ./...
go test -race ./...
go test -race -tags=integration ./internal/storage/postgres ./internal/analysis -count=1
```

The integration invocation loaded private test settings from `/tmp/evia-test.env` and `/tmp/evia-pipeline.env` without copying their values into the repository. PostgreSQL package time was **38.157s**; ClickHouse analysis package time was **6.189s**. These are test-package durations, not endpoint latency or capacity measurements. Missing real dependencies fail the integration checks.

Repository-root checks also passed:

```sh
/tmp/evia-api-verify-venv/bin/python tooling/verify_contracts.py
python3 tooling/check_naming.py
```

The contract check uses the prepared verification virtual environment; the default Python lacks `openapi_spec_validator`. Repeated TypeScript generation compared equal with `cmp`, and standalone TypeScript compilation of the generated API types passed. After combining the console changes and these generated types, full-workspace TypeScript and formatting checks also passed. These checks establish type/format consistency, not a console build or browser use of the new endpoints.

Independent review also reproduced a datetime-validation mismatch before the fix: malformed strings passed dry validation but failed SQL planning. Both paths now share RFC3339Nano parsing. Scalar, membership-array and nested funnel predicates reject invalid dates while preserving UTC, offset and nanosecond values. After the fix, affected analysis/reporting race tests passed, the real PostgreSQL no-write validation regression passed (**1.453s**), and the real ClickHouse analysis package passed again (**6.250s**). Affected-package vet, formatting and naming checks passed. These regressions were observed failing before the production change.

## Evidence by boundary

| Boundary | Verified behavior |
| --- | --- |
| Analysis unit tests | Reject mixed selectors, empty/duplicate/multi-version selections and fields outside selection; bind exact event/version pairs without a cross-product. |
| Real ClickHouse | Mixed v1/v2 facts produce v1 count/UID UV **2/1**, v2 **2/2**, selected pairs **3/2**; legacy event-key selection remains **4/3**. Project/environment facts are isolated; details and journey respect selection. |
| Real PostgreSQL | Published environment membership is required even for count without properties; selection survives enqueue/claim; catalog excludes drafts and other environments, preserves immutable metadata/order, and derives activation/readiness separately. |
| Real PostgreSQL validation | Validates false/zero and rejects wrong types, unpublished versions, invalid field scope and forbidden operations. Catalog/job/audit/outbox table counts remain unchanged. Viewers cannot validate details; outsiders and revoked members are denied. |
| HTTP handlers (`httptest`) | Response contracts, scoped cursors, authentication, CSRF/Origin, strict input and safe failures; source bearer does not authorize discovery. |
| Runtime configuration + control handler (`httptest`) | Discovery/validation remain registered with execution disabled or enabled. Availability flags match configured engine; validation creates no job and calls no execution engine. Query submission routes follow execution configuration. |

The PostgreSQL readiness projection fixture uses the existing synthetic probe adapter in an isolated schema; it verifies state projection, not a new real broker readiness run. Validation scans no facts, performs no cardinality/cost simulation and reserves no execution budget. Its hashes do not freeze data, grant authority or pin `event_keys`/`all_events` selection.

## Remaining evidence and delivery

The existing API process on port 8080 was unchanged. No new network-running HTTP acceptance, browser flow, frontend interaction or performance result is claimed. Saved-analysis drafts/immutable versions, publication diff/idempotency, dashboard configuration and durable shared-budget coordination, machine principals and their API remain pending. Existing project/query permissions and limits still apply; execution revalidates after dry validation. The full platform and ten-million-user capacity target remain unverified.
