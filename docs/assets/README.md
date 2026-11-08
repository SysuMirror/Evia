# README figures

These figures were generated with ImageGen and reviewed against the repository on 2026-11-08 (Asia/Shanghai). They illustrate logical relationships, not measured performance or production capacity. PNG originals are committed locally so the README does not depend on an external image host.

| Figure                                                     | Meaning                                                                                           | Authoritative references                                                                                                     |
| ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| [Event data architecture](event-data-architecture.png)     | Existing control, ingestion and query flows, with planned SDK, identity processing and dashboards | [Architecture](../architecture.md), [progress](../../PROGRESS.md), [backend evidence](../verification/backend-2026-11-07.md) |
| [Versioned analytics model](versioned-analytics-model.png) | Existing event release lifecycle and proposed reporting version dependencies                      | [Releases](../releases.md), [dashboard proposal](../plans/custom-dashboards.md)                                              |

## Semantics to preserve

- Solid module borders mean implemented, dashed borders mean planned. Text labels make status understandable without relying on color.
- PostgreSQL owns metadata, jobs and bounded query results. Redis provides admission budgets. Kafka buffers events; the ingestion worker writes ClickHouse; the query worker reads it. Repeated database symbols represent the same storage role, not separate instances.
- The React console currently implements login and project access pages. Existing backend APIs have broader capabilities; the figure does not imply those pages exist.
- Published definitions remain immutable. Real Kafka/ClickHouse delivery verification yields a time-limited readiness lease; activation is explicit. Publishing alone does not authorize ingestion.
- Metric versions, saved analyses, dashboard versions and the shared reporting API for users and agents are proposed. They are not implemented by adding this diagram.

## Updating the artwork

Use ImageGen with a white landscape canvas, legible English labels, consistent navy outlines, restrained blue and teal, aligned panels, solid existing modules and dashed planned modules. Keep legends and short notes. Do not add decorative plots, fabricated metrics or capacity claims.

For the architecture figure, retain three independent logical lanes: control, ingestion and analysis. Connect Redis only to ingestion admission. Keep planned extensions in a separate region.

For the versioning figure, retain two panels: the existing draft/validate/publish/verify/activate lifecycle, and the proposed schema/metric/analysis/dashboard dependency chain. Keep the shared reporting API inside the planned region.

After regeneration, inspect every label, arrow and border against the linked sources. Update README alternative text and captions when semantics change. Numeric evidence belongs in reproducible plots generated from actual measurements, with units and conditions recorded separately.
