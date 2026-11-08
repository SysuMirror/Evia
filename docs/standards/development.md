# Development and verification

Implement one reviewable responsibility per commit. Use `feat`, `fix`, `refactor`, `test`, `docs`, `build` or `chore`, an optional scope and a concrete imperative subject. Keep code and its necessary tests together. Split independent changes; never use file-count limits to separate a change from the tests proving it.

Frontend dependency directions and state ownership are defined in [frontend modules](frontend-modules.md). `make check` includes package-boundary and runtime-cycle checks, plus benchmark-tool policy tests. Split modules by responsibility; moving all state into a single large hook or passing the entire controller through every view does not establish a useful boundary.

Current delivery prioritizes backend modules: contract, domain policy, persistence adapter, HTTP transport and real-dependency verification. Coordinated frontend work follows, using shared components and generated transport types. Feature completeness includes failures, concurrency, empty states, permissions and recovery; this sequencing does not reduce those requirements.

Tests are classified by evidence:

- Unit: pure policies, validation, version comparison, identity boundaries and deterministic query semantics.
- HTTP: parsing, authentication, authorization, response format, pagination and error mapping with isolated collaborators.
- Contract: golden and negative examples checked against the same authoritative schema across languages.
- Integration: real PostgreSQL/Kafka/ClickHouse behavior, transaction atomicity and recovery; explicitly enabled and never silently replaced by mocks.
- Browser: real forms, routing, keyboard access and backend interaction using a dedicated test environment.
- Load/recovery: fixed hardware, data distribution, latency, backlog and failure evidence. Do not extrapolate an unmeasured production capacity.

Use the [benchmark runbook](../runbooks/benchmark.md) for isolated ingestion/query measurements. Preserve failed baselines and compare the same dataset semantics and explicit resource settings after fixes. A response receipt, durable queue acknowledgement, visible deduplicated fact and completed query are separate observations.

Use parameterized SQL and explicit transactions; migrations have a single owner and ordered ledger. Never run destructive schema changes implicitly on application startup. Use injected clocks and ID sources only where determinism is needed; avoid global test overrides.

Commit messages and PROGRESS.md report what actually ran. Any skipped database, browser or production check remains an explicit open item.
