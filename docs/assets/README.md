# README figures

These figures were generated with ImageGen. The event-data figure was reviewed on 2026-11-08 and the versioned-analysis figure was regenerated and reviewed on 2026-11-09 (Asia/Shanghai). They illustrate logical relationships, not measured performance or production capacity. PNG originals are committed locally so the README does not depend on an external image host.

| Figure                                                     | Meaning                                                                                           | Authoritative references                                                                                                     |
| ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| [Event data architecture](event-data-architecture.png)     | Existing control, ingestion and query flows, with planned SDK, identity processing and dashboards | [Architecture](../architecture.md), [progress](../../PROGRESS.md), [backend evidence](../verification/backend-2026-11-07.md) |
| [Versioned analytics model](versioned-analytics-model.png) | Implemented event lifecycle and directly pinned saved analyses, with planned reporting extensions                      | [Releases](../releases.md), [saved analyses](../saved-analyses.md), [dashboard proposal](../plans/custom-dashboards.md)                                              |

## Semantics to preserve

- Solid module borders mean implemented, dashed borders mean planned. Text labels make status understandable without relying on color.
- PostgreSQL owns metadata, jobs and bounded query results. Redis provides admission budgets. Kafka buffers events; the ingestion worker writes ClickHouse; the query worker reads it. Repeated database symbols represent the same storage role, not separate instances.
- The control lane illustrates console access and API responsibilities. The console now also exposes event, release, source and analysis workspaces; the small access label is not an exhaustive page inventory. Consult PROGRESS.md for current feature evidence.
- Published definitions remain immutable. Real Kafka/ClickHouse delivery verification yields a time-limited readiness lease; activation is explicit. Publishing alone does not authorize ingestion.
- Saved analyses directly pin immutable published event versions. Draft validation/diff is read only; publication is immutable; execution binds real time/timezone and uses the existing shared query budget and current session authorization.
- The metric registry, dashboard layouts and machine-authenticated Agent API are planned, shown separately with dashed borders. The implemented saved-analysis session API does not imply a machine bearer API or a new console editor.

## Updating the artwork

Use ImageGen with a white landscape canvas, legible English labels, consistent navy outlines, restrained blue and teal, aligned panels, solid existing modules and dashed planned modules. Keep legends and short notes. Do not add decorative plots, fabricated metrics or capacity claims.

For the architecture figure, retain three independent logical lanes: control, ingestion and analysis. Connect Redis only to ingestion admission. Keep planned extensions in a separate region.

For the versioning figure, retain three panels: implemented event draft/validate/publish/verify/activate; implemented directly schema-pinned saved analysis draft/validate-and-diff/publish/bind-time-and-zone/shared-query-job through the session API; and planned metric registry/dashboard layouts/machine-authenticated Agent API. Do not insert the future metric registry as a required dependency of current saved analyses.

After regeneration, inspect every label, arrow and border against the linked sources. Update README alternative text and captions when semantics change. Numeric evidence belongs in reproducible plots generated from actual measurements, with units and conditions recorded separately.

The 2026-11-09 versioned-analysis PNG was edited with ImageGen using the previous local figure, then visually checked for every label, arrow and implementation border. The repository copy is 1536 × 1024 pixels; SHA-256: `61b10a5f26f5929d706e471d86d3ce4e596f98124f88c3a585f2bb8b1cdb9133`.
