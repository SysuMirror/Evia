---
name: platform-ui
description: Build complete Evia console features with shared design components and real API behavior.
---

Internal maintenance only; do not ship this file in customer integration packages.

Read AGENTS.md and docs/standards/ui.md. Complete and verify backend behavior first. Build shared primitives, tokens and layouts in packages/design-system before composing apps/console pages. Use generated contracts and the shared API client.

Preserve project context and readable hierarchy. Every asynchronous surface needs loading, empty, success, permission and error handling. Forms retain input on failure; concurrent edits remain recoverable. Include visible labels, field feedback, focus management, keyboard navigation and responsive layouts. Avoid decorative metrics or controls for unimplemented behavior.

Run type checking and production build, then verify the feature in a real browser against a real backend. Exercise both successful and failed operations, keyboard interaction and narrow viewports. Update PROGRESS.md with the actual evidence and commit the reusable foundation separately from business pages.
