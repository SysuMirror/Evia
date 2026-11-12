# Release TypeScript artifacts

`GET /api/v1/projects/{project_id}/releases/{release_id}/artifacts/typescript` renders a deterministic TypeScript declaration for one immutable release. It is a read-only, member-level endpoint backed by the release manifest stored in PostgreSQL; no event data leaves the catalog boundary and the response never includes credentials or secrets.

## Why deterministic generation

A release manifest is immutable and already contains every published event schema. The artifact is generated on demand from that manifest through `internal/artifacts` instead of being stored as a separate mutable file, so:

- The output bytes depend only on the manifest content — there are no timestamps, host names or request-scoped values.
- The same manifest always produces the same source and the same `sha256` checksum, which lets integration tools pin and verify artifacts.
- Unpublished drafts can never appear: only events frozen inside the release manifest are rendered.

## Supported schema subset

The catalog publication gate deliberately accepts a small JSON Schema subset: flat objects with at most 50 properties, scalar types (`string`, `number`, `integer`, `boolean`), homogeneous scalar arrays, and string/number/boolean enums. References, combinators and nested objects are rejected before publication. The generator mirrors exactly this subset:

- `string` → `string`, `number`/`integer` → `number` (all values stay within JavaScript safe-integer range, so `bigint` would be incorrect), `boolean` → `boolean`, scalar arrays → `T[]`.
- Enums become sorted literal unions; required properties are non-optional, all others use `?`.
- Each event key renders a PascalCase `XxxProperties` interface; snake_case keys map injectively to distinct names, and a collision would fail generation rather than emit ambiguous code.
- The module also exports an `eventKeys` readonly tuple, an `EventKey` union and an `EventPropertyMap` keyed by event key.

Any construct outside the subset fails generation with an `ARTIFACT_*` fault instead of producing misleading TypeScript. Each event's compatibility entry reports its interface name, identifier validity and unsupported constructs.

## Response and integrity

The JSON envelope carries the release coordinates, `language` (`typescript`) and `language_version`, event count, byte size, per-event compatibility metadata, the generated `source`, and its `checksum`. Clients can verify the checksum locally before committing the file to an SDK repository. The console release detail modal renders the source in a code block, offers copy and a client-side `.ts` download, and surfaces a retryable error state when generation is unavailable.

## Evidence

- Generator unit tests cover every supported type, enums, required/optional fields, ordering, determinism and fail-closed rejection of unsupported constructs.
- Real PostgreSQL integration tests load a published release manifest, run the generator, and verify authorization (member read, outsider invisibility, missing/malformed release handling).
- Full-stack evidence: a release created through the real API produced an artifact whose checksum matches `sha256(source)`, and the generated source compiles under `tsc --strict`, including a negative check that an object missing required properties is rejected.
- HTTP boundary checks confirm 401 (no session), 400 (malformed ids/query), 404 (unknown release) and 405 (non-GET methods).

## Known limits

- Only TypeScript declarations are generated; other languages and a packaged, versioned SDK registry are separate work.
- Artifacts describe event properties only; envelope fields (`event_id`, `occurred_at`, identity, context, SDK metadata) are governed by the stable collection contract in `contracts/event`.
- The JSON Schema subset is intentionally bounded; richer schema support would require extending both the publication validator and this generator together.
