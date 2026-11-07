---
name: platform-ui
description: Build complete Evia console features with shared design components and real API behavior.
---

Internal maintenance only; do not ship this file in customer integration packages.

Read AGENTS.md and docs/standards/ui.md. Complete and verify backend behavior first. Build shared primitives, tokens and layouts in packages/design-system before composing apps/console pages. Use generated contracts and the shared API client.

Ordinary UI controls must use Evia-owned React components and semantic HTML. Follow docs/plans/native-ui.md when replacing the interim Ant Design implementation. Put tokens in CSS variables, use native control props and explicit React state, and keep forms based on native submission/validation with application field errors. Do not keep Ant Design hidden underneath renamed wrappers or reproduce its imperative form engine. Retain the existing router, server-state and API boundaries. Mature specialized libraries are appropriate for complex capabilities such as charts when their accessibility, maintenance and bundle impact are documented; load them on demand behind shared Evia components.

Design for a compact developer console: cool whites, slate text, restrained blue accents, readable tables and real actions. Reuse DesignProvider, Brand, WorkspaceShell, AuthLayout, PageHeading, DataSurface and PaginationBar. Desktop header is 64 px, sidebar 220 px and content padding 24 px; page titles are 22–24 px. Use the approved logo asset, not a substitute letter mark. Use integrated white resource panels with soft 20 px corners on a light gray canvas, quiet gray navigation pills and one vivid primary action. AuthLayout uses a restrained headline, original pastel abstract visual and white rounded form on desktop, collapsing to the form on mobile. Borrow layout principles from approved references without copying another brand, photographs or navigation. Avoid promotional paragraphs, decorative data cards and mock metrics.

Page headings contain the title and relevant actions. Do not add English eyebrows, generic explanatory subtitles, sidebar explanations or duplicated table-section titles. Remove stable UUIDs from ordinary resource lists; keep them copyable in details. Put useful help next to the relevant control, in an empty state or in recovery feedback. Use direct Chinese copy with commas between parallel status facts, no middle-dot separators. Document titles use the plain brand “Evia 映途”.

Preserve project context and readable hierarchy. Every asynchronous surface needs loading, empty, success, permission and error handling. Forms retain input on failure; concurrent edits remain recoverable. Include visible labels, field feedback, stable accessible names for busy actions, focus management, safe confirmation defaults, keyboard navigation and responsive layouts. Keep every implemented action reachable on mobile; table overflow stays inside its surface.

Run type checking and production build, then verify the feature in a real browser against a real backend. Exercise successful and failed operations, keyboard interaction and narrow viewports; assert no uncaught browser exceptions. Visually inspect fresh desktop and mobile screenshots for density, hierarchy, overflow and dialogs before claiming completion. Update PROGRESS.md with actual evidence and commit the reusable foundation separately from business pages.

For the native migration, capture the current production bundle and interaction baseline first. Compare initial JavaScript/CSS, lazy chunks and browser measurements under the same conditions after migrating. Document conditions and results, investigate regressions, and avoid treating dependency removal or a raised warning threshold as performance evidence. Keep pending migrations explicit in PROGRESS.md.
