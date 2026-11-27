# Architecture and delivery boundaries

Evia follows a modular self-hosted design: one Go backend module hosting API and worker subcommands, a TypeScript workspace for the React console, design system, generated API client and zero-dependency browser SDK, separately packaged internal and public agent assets, and an independent stream-processing project planned for future stateful Flink workloads.

Control-plane APIs, event ingestion, Kafka-to-ClickHouse pipelines, distributed Redis budgets, multi-model analytics, versioned metrics/dashboards/reports, identity graph resolution, encrypted content handling, end-to-end A/B experimentation and data lifecycle workers are delivered with real-dependency verification. Remaining production scale and external-environment items remain documented in `PROGRESS.md` and `docs/production-readiness.md`.

## Ownership

- `access` owns accounts, sessions, OAuth2 PKCE login, project invitations, memberships, settings, audit trails and machine credentials.
- `catalog` owns event and property definitions, schema validation, mutable drafts and immutable releases.
- `delivery` owns release readiness probes, activation leases, signed public configuration (`/v1/config`) and bounded rendered config caching.
- `ingestion` owns source admission, distributed collection budgets, built-in `sys.*` catalog rules, queue acknowledgement and rejection diagnostics.
- `pipeline` owns Kafka producers/consumers, ClickHouse raw fact writes, erasure barrier filtering, property-slot materialization and consumer lag sampling.
- `analysis` owns typed filtering, query plans and ClickHouse execution for count, UV, trend, journey, funnel, retention, segment, path, attribution, heatmap and experiment evaluation.
- `experiments` owns A/B experiment configuration, lifecycle transitions, deterministic allocation rules and public-config exposure metadata.
- `reporting` and `reports` own capability discovery, versioned atomic and formula metrics, saved analyses, versioned dashboards and scheduled reports.
- `identity` owns project-scoped identity graph folding, immutable revision snapshots, retention pruning and `resolved_person` expansion.
- `content` owns encrypted chunk assembly, bounded access controls and audited sensitive content reads for session replay.
- `erasure`, `retention`, `archiving`, `exporting` and `backfilling` own compliance subject erasure, tombstone barriers, retention sweeps, cold archive manifests, CSV/NDJSON/Parquet exports and controlled backfill jobs.
- `overview`, `quotas`, `diagnostics`, `propertycatalog`, `propertylayout` and `artifacts` own bounded project summaries, quota overrides, pipeline health diagnostics, hot-key column planning and bounded artifact access.

HTTP adapters translate generated wire types into application inputs. Application services depend on consumer-defined ports. Persistence adapters implement atomic policies in explicit transactions. No domain directly updates another domain's tables.

Control metadata and job queues belong to PostgreSQL. Redis enforces cross-instance collection budgets. Kafka and ClickHouse form the durable ingestion and analytical path (with Flink reserved as a separate planned project for stateful stream processing). Real integration and capacity evidence must precede production scale claims.

## Agent assets

Platform development uses root/scoped `AGENTS.md` and `.agents/skills/platform-*` assets. Business integration uses `integrations/skills/activity-*` and public SDK/CLI/API contracts. Public packages use an allowlist, resolve their entire dependency closure and preserve the business repository's own instructions. Platform-specific maintenance material never enters the business integration package.
