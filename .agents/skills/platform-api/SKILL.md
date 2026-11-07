---
name: platform-api
description: Maintain Evia management and collection APIs using authoritative contracts and consistent security boundaries.
---

This is an internal platform development skill. It is not part of the business integration distribution.

1. Read the root and scoped AGENTS.md, docs/standards/api.md and the relevant contracts. Identify implemented versus planned operations in PROGRESS.md.
2. Change OpenAPI or JSON Schema first. Regenerate wire types and offline validators with tooling/codegen/generate.py; format Go output. Never hand-edit generated files.
3. Keep policy, persistence and HTTP separate. Authorize project scope before access; preserve transaction, revision, idempotency and audit boundaries. Do not claim durable acceptance or ingestion readiness before the relevant system confirms it.
4. Use the shared HTTP boundary. Preserve resource/list/problem formats, limits, safe errors and request IDs. Test unauthorized callers, malformed input, stale writes and dependency failures.
5. Run contract verification, gofmt, go vet and race tests. Run explicitly enabled real-database tests for persistence changes. Report skipped evidence accurately.
6. Commit a cohesive backend change with its categorized test evidence. In the current backend-first phase, continue independent backend modules before coordinated frontend delivery; record unfinished frontend and integration work in PROGRESS.md.
