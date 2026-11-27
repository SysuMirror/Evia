# README figures and console screenshots

The conceptual architecture diagram (`event-data-architecture.svg` / `event-data-architecture.png`) is authored as pure vector SVG in an academic systems-paper style and rendered at 2x DPI (2800 × 1560 PNG) on 2026-11-27 (Asia/Shanghai) to align with the migration `054_experiments.sql` platform baseline (control and collection APIs under a unified Go runtime, Redis admission budgets, PostgreSQL/Kafka/ClickHouse, query and governance workers, A/B experimentation, identity graph, session replay, and separately planned Apache Flink stateful stream processing). The earlier versioned-analysis figure is retained as a historical illustration and is no longer embedded in the README. All originals are committed locally so the README does not depend on an external image host.

| Figure | Meaning | Authoritative references |
| --- | --- | --- |
| [Event data architecture](event-data-architecture.png) ([SVG](event-data-architecture.svg)) | Current logical architecture (2800 × 1520 PNG, SHA-256 `db1aef8d673602a6cd715f5af9ca2699300fa99611eea5fb0eae79f3f4c06a4e`; SVG SHA-256 `c3bd54e564dbf4829abd1b34b0c72086f30bbc228b7817bc7998dc2991b69789`): implemented control/query plane, event ingestion data plane, six governance/lifecycle workers, and planned Apache Flink stream processing | [Architecture](../architecture.md), [progress](../../PROGRESS.md), [feature coverage](../feature-coverage.md) |
| [Versioned analytics model](versioned-analytics-model.png) | Historical pre-dashboard snapshot of the event and saved-analysis lifecycle | [Releases](../releases.md), [saved analyses](../saved-analyses.md), [dashboard proposal](../plans/custom-dashboards.md) |

## Current console captures (2026-11-27)

Captured on 2026-11-27 (Asia/Shanghai) against the local full-stack production console bundle (`apps/console/dist/`) backed by real `evia serve`, `ingest-worker`, `query-worker`, PostgreSQL (`054_experiments.sql`), Kafka, ClickHouse, and Redis using a synthetic demo project (`Acme Commerce`). No credentials or real business user data appear:

| Asset | Viewport / Size | SHA-256 | Contents |
| --- | --- | --- | --- |
| [landing-desktop.jpg](landing-desktop.jpg) | 1920 × 1200 JPEG (from 1440 × 900 @2x) | `e25b7c5dcf435ef4ee9b7a666299152d9b3fa1fbd21dd3e8ce2aad44199ae1ac` | Public landing page hero and privacy positioning |
| [overview-desktop.png](overview-desktop.png) | 1440 × 900 @2x PNG | `04bf515bdbb688138b3d7254fd34c22bf0596aea56d30ff6fce70ad48628f1f2` | Project overview with 7-day synthetic trend, KPI cards, and top events |
| [analysis-desktop.png](analysis-desktop.png) | 1440 × 900 @2x PNG | `dba7a9e17016753945bc86fb9da99da98e25ad0e357eda2ac41a7a039f8d5ea1` | Multi-model analytics workbench showing a 3-step ordered tapered funnel and step-over-step conversion table |
| [retention-desktop.png](retention-desktop.png) | 1440 × 900 @2x PNG | `c99c8498299572f1c54e219f08f2aca931bde54adf6f9f665bfb8325bffbec48` | Cohort retention analysis workbench with color-coded daily retention heatmap matrix |
| [experiments-desktop.png](experiments-desktop.png) | 1440 × 900 @2x PNG | `1a2d2f52b8ce4048db25ea1abfe7747cf067e9f4b409174c8bdad02d41e44d63` | A/B experimentation workspace with SRM check (`p = 0.5839`), Lift & 95% CI Forest Plot, and two-proportion Z-test results |

## Semantics to preserve

- Solid module borders mean implemented, dashed borders mean planned. Text labels make status understandable without relying on color.
- PostgreSQL owns metadata, jobs and bounded query results. Redis provides admission budgets. Kafka buffers events; the ingestion worker writes ClickHouse; the query worker reads it. Repeated database symbols represent the same storage role, not separate instances.
- The control lane illustrates console access and API responsibilities. The console now also exposes event, release, source and analysis workspaces; the small access label is not an exhaustive page inventory. Consult PROGRESS.md for current feature evidence.
- Published definitions remain immutable. Real Kafka/ClickHouse delivery verification yields a time-limited readiness lease; activation is explicit. Publishing alone does not authorize ingestion.
- Saved analyses directly pin immutable published event versions. Draft validation/diff is read only; publication is immutable; execution binds real time/timezone and uses the existing shared query budget and current session or explicitly granted machine authorization.
- Dashboards pin saved-analysis versions and enqueue their child queries atomically. Workers execute these queries independently; the figure does not imply an atomic cross-widget result snapshot. The independent atomic metric backend now exists in reporting and reuses the same query path; direct metric references in saved analyses and dashboards remain planned. The architecture figure does not enumerate every reporting resource.
- Machine administration uses human sessions and CSRF; business agents use scoped machine Bearer credentials. Workers recheck the originating credential at claim, lease checks and completion. Public source configuration remains PostgreSQL-backed; those metadata dependencies are omitted to keep the diagram readable. API boxes represent logical roles in the same Go runtime.

## Updating the artwork

Use ImageGen with a white landscape canvas, legible English labels, consistent navy outlines, restrained blue and teal, aligned panels, solid existing modules and dashed planned modules. Keep legends and short notes. Do not add decorative plots, fabricated metrics or capacity claims.

For the architecture figure, keep event data separate from management/query coordination. Connect Redis only to collection admission. Show both PostgreSQL job claiming and result/status persistence, and keep planned extensions in a separate region. The current PNG SHA-256 is `fd150af4681325194d4b9d06ae28cb74d6f0bec6e88d4cdd8a73fe4bf525360c`.

The historical versioning figure retains three panels: implemented event draft/validate/publish/verify/activate; implemented directly schema-pinned saved analysis draft/validate-and-diff/publish/bind-time-and-zone/shared-query-job through the session API; and planned metric registry/dashboard layouts/machine-authenticated Agent API. This old figure is not embedded as a current capability inventory. Do not insert the future metric registry as a required dependency of current saved analyses.

After regeneration, inspect every label, arrow and border against the linked sources. Update README alternative text and captions when semantics change. Numeric evidence belongs in reproducible plots generated from actual measurements, with units and conditions recorded separately.

The 2026-11-09 versioned-analysis PNG was edited with ImageGen using the previous local figure, then visually checked for every label, arrow and implementation border. The repository copy is 1536 × 1024 pixels; SHA-256: `61b10a5f26f5929d706e471d86d3ce4e596f98124f88c3a585f2bb8b1cdb9133`.

## Console captures

`saved-analysis-desktop.png` and `saved-analysis-mobile.png` are unedited browser captures, not generated artwork. They show synthetic operation-grouped events read through the actual session API, PostgreSQL job queue and ClickHouse query worker. Both show immutable saved analysis v3 with count/UID results of 1/1 and 2/2. No credentials or business data appear.

Captured on 2026-11-09 (Asia/Shanghai), with reduced motion and viewports 1440 × 1000 and 390 × 844. The production entry is `index-Cy7ZlfFY.js`; responsive chart categories and the horizontally scrollable result table retain the same data. Verification and remaining limits are recorded in [the saved-analysis evidence](../verification/saved-analyses-2026-11-09.md).

## Measured benchmark plot

`load-2026-11-08.svg` is a Matplotlib plot of [actual benchmark evidence](../verification/data/load-2026-11-08.json), generated by `tooling/benchmark/plot_report.py`. It contains measured receipt throughput and successful count-query latency samples. It is not generative artwork; no values or uncertainty bounds were invented. The conditions and comparison limits are described in [the load report](../verification/load-and-maintainability-2026-11-08.md).

With optional Matplotlib installed, regenerate from the repository root using `MPLCONFIGDIR=/tmp/evia-matplotlib python3 tooling/benchmark/plot_report.py`. Inspect labels, units, sample definitions and figure notes against the data before committing an update.

`capacity-2026-11-09.svg` plots the complete two-worker million-event matrix from [the subsequent measured evidence](../verification/data/capacity-2026-11-09.json). Panel A includes every first-observed and repeated attempt, with failed attempts shown explicitly. Panel B uses only successful-sample end-to-end p95 at client concurrency 1, 4 and 8; it does not imply those workloads were free of failures. The separate smaller single-worker profile is not mixed into this figure.

Regenerate with `MPLCONFIGDIR=/tmp/evia-matplotlib python3 tooling/benchmark/plot_query_capacity.py`. This Matplotlib figure uses a fixed SVG identifier salt and omits generated date metadata. The rendered PNG preview was visually inspected for labels, units, legends and clipping, and outcome counts were checked against the JSON. Conditions, failed queries and deployment limitations are documented in [the query capacity report](../verification/query-capacity-2026-11-09.md).

`dashboard-desktop.png` and `dashboard-mobile.png` are unedited captures from the real dashboard/SDK browser workflow on 2026-11-09. Production entry `/assets/index-C_5Zmr32.js` uses API/runtime migration 009. Two published analysis widgets show count 3 and raw-UID distinct count 2 from synthetic facts. The mobile chart fits its card; only wide result tables scroll horizontally. See the [verification record](../verification/dashboards-sdk-2026-11-09.md) for failed baselines, revised layout bounds and final checks.

`machine-access-desktop.png` and `machine-access-mobile.png` are unedited metadata-only captures from the final 45-case production browser suite on 2026-11-09 (Asia/Shanghai). Entry `/assets/index-CNiZv9Q6.js` uses the migration-010 runtime. The desktop shows active/expired credentials; the later mobile capture shows the same synthetic principal after revocation. No secret values appear. Browser timestamps use the test browser timezone. The detail dialog scrolls vertically, while wide credential tables own their horizontal scrolling. See [machine-access verification](../verification/machine-access-2026-11-09.md).

`metric-grants-desktop.png` is an unedited 1440 × 900 capture of the existing machine creation form, from the subsequent 45-case production suite on 2026-11-09. Entry `/assets/index-CFt0DjHa.js` uses migration 012. Four metric permissions were selected with the keyboard and saved through the real API; the screenshot contains no credential. This is not a metric management page. The modal scrolls vertically while its actions remain visible. See [metric verification](../verification/metrics-2026-11-09.md).
