---
name: platform-verification
description: Verify Evia changes at the relevant unit, contract, persistence and browser boundaries.
---

Internal maintenance only. Read docs/standards/development.md and relevant AGENTS.md files.

Choose checks that can disprove the behavior under change. Cover domain policy with unit tests, wire semantics with contract and HTTP tests, SQL and concurrency with real PostgreSQL integration tests, and user interaction with browser tests. Dependency tests must fail clearly when explicitly requested without configuration; never replace them silently with mocks.

Use isolated test schemas and synthetic accounts. Avoid production credentials, user data or business operations. Keep test environments distinct from deployment configuration. Record test commands and outcomes in PROGRESS.md. A local passing test is not production capacity evidence. Run broader checks only after changes or failures justify them.
