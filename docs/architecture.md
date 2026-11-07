# Architecture and delivery boundaries

Evia follows the accepted design 1.2: one Go backend module, independent stream-processing project, a TypeScript workspace for console/SDK/tools and separately packaged internal and public agent assets.

The initial implementation focuses on project access and event definition, validation and release. Each module is completed through backend, frontend and verification. Later R1 requirements remain in PROGRESS.md; this order is not a reduction of the platform's scope.

## Ownership

- access owns users, sessions, projects and permissions.
- catalog owns event definitions, mutable drafts, immutable releases and activations.
- ingestion owns source admission, budgets, queue acknowledgement and rejection diagnostics.
- identity owns project-scoped relationships, login/logout boundaries and conflicts.
- analysis owns typed filtering, query plans, PV/UV, journey and ordered funnel semantics.
- lifecycle owns retention, archive manifests, erasure and recovery barriers.

HTTP adapters translate generated wire types into application inputs. Application services depend on consumer-defined ports. Persistence adapters implement atomic policies in explicit transactions. No domain directly updates another domain's tables.

Control data belongs to PostgreSQL. Kafka/Flink/ClickHouse form the eventual event processing and analytical path. No per-event PostgreSQL lookup is permitted in the planned high-volume collector. Real integration and capacity evidence must precede production claims.

## Agent assets

Platform development uses root/scoped AGENTS.md and .agents/skills/platform-* assets. Business integration uses integrations/skills/activity-* and public SDK/CLI/API contracts. Public packages use an allowlist, resolve their entire dependency closure and preserve the business repository's own instructions. Platform-specific maintenance material never enters the business integration package.
