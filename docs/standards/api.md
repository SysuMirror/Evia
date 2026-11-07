# API conventions

OpenAPI and JSON Schema under contracts are the only wire-format authority. Changes update generated types, fixtures and tests together.

- Paths use /api/v1 for control and query APIs; /v1 for public collection/configuration. HTTP method expresses operation intent.
- JSON properties use snake_case. IDs are strings, timestamps RFC3339 UTC. Numeric values stay within JavaScript's safe range. Missing, null, zero and false are distinct.
- Success returns the resource directly. Lists return `{items, next_cursor}` with a bounded limit, stable ordering and opaque scoped cursors. Do not wrap every response in an unrelated status/data format.
- Errors return `{code, message, request_id, details?}`. HTTP status is meaningful; business failures never masquerade as HTTP 200. Field paths identify invalid input without echoing secret values.
- Return a server-generated X-Request-ID consistently. Use stable uppercase error codes; safe messages for callers and structured context for operator logs. Never log passwords, cookies, write credentials or full event bodies.
- Bound body size, JSON depth, arrays, properties, query time and page size. Reject unknown fields, duplicate JSON keys and trailing JSON values. Validate before side effects.
- The shared HTTP boundary limits cooperative dependency work to 20 seconds and preserves earlier caller deadlines. Collection uses a shorter five-second dependency deadline. Socket write timeouts do not substitute for context cancellation; long-running analysis uses asynchronous jobs.
- Authenticate management calls with the defined session contract and CSRF protection. Authorize every project operation. Browser write keys never grant management or query privileges.
- Mutations use revisions/If-Match where defined. Concurrent changes fail explicitly. Idempotency keys bind actor, scope, operation and canonical request content; reusing a key with different content is a conflict.
- Published releases are immutable. Creating a release, distributing it and activating it are distinct results. Acceptance into a durable queue is not query visibility or business success.
- Expose only implemented operations. Every new handler includes contract, permission, invalid-input and failure-path tests.
