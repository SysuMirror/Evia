# Console design conventions

Evia is a developer console. Optimize for finding a resource, understanding its state and taking a real action. Use compact, calm layouts with cool white surfaces, slate text and a restrained blue accent. Do not add marketing copy, decorative metrics, oversized cards or controls for capabilities that do not exist.

## Shared foundation

Build reusable tokens, layouts and interaction patterns in packages/design-system before composing business pages in apps/console. Evia-owned React components and semantic HTML are the default for ordinary controls. DesignProvider owns Evia theme context and shared CSS variables for color, typography, spacing, density and focus. Existing access pages now use this native foundation; subsequent delivery follows the [native UI plan](../plans/native-ui.md). Brand uses the approved image asset with the plain text “Evia 映途”; do not invent letter marks or hand-draw substitute logos. WorkspaceShell owns the 64 px header and 220 px sidebar. Desktop content uses 24 px padding; mobile uses 16 px padding and keeps all actions reachable.

Build buttons, fields, inputs, selects, checkboxes, alerts, dialogs, tabs, tables, pagination and notifications in the shared package. Prefer native control props, semantic elements and explicit React state. Forms use native submission and validation with application-owned field errors. A shared component wrapping Ant Design does not complete the migration; remove its underlying runtime and styles after its consumers migrate. Keep router, server-state and generated API responsibilities in their existing packages.

Use a mature specialized dependency when a complex capability such as chart rendering warrants it. Record the capability, accessibility support, maintenance cost and measured bundle impact in the introducing PR. Encapsulate it behind an Evia component and load it only where needed. Ordinary page controls must not introduce a new general-purpose UI library. Prefer native accessibility behavior, especially dialog focus and keyboard handling, over a custom interaction framework.

PageHeading contains a 22–24 px title and the actions for that page. Do not add an English eyebrow, a generic explanatory subtitle or a second heading that repeats the page title. Navigation is for destinations, without explanatory paragraphs. Explain a consequence next to the control where the user makes that decision, or in a useful empty state, error or help message. Use direct Chinese copy; separate parallel status facts with commas, never middle dots. Document titles use “Evia 映途”.

Clarity is not an instruction to remove visual hierarchy or useful context. Use deliberate typography, aligned controls, grouped surfaces, descriptive resource states and purposeful icons. In an analysis workspace, the primary result and run action must be discoverable without scrolling past a full configuration form on a normal desktop viewport. Keep core selectors in a compact toolbar; move complex time, filter, precision and funnel editors into labeled configuration dialogs. Show the current query summary beside these controls and retain the submitted snapshot's own context with its result. Do not manufacture cards, sample metrics or empty sections to make a page look populated.

Use DataSurface for a resource table and PaginationBar for bounded pagination. Prefer readable rows to cards. Keep stable identifiers in copyable details unless comparing identifiers is the user's current task. Show only relevant columns and implemented actions. Use a single primary action, secondary actions beside it, minimal borders, soft 20 px panel corners and minimal shadows. Use an eight-pixel spacing rhythm with four-pixel adjustments; preserve useful density without shrinking text or click targets beyond readability.

AuthLayout places a restrained brand headline and original soft abstract visual beside a rounded white sign-in form on a very pale blue and pink background. Collapse to the form alone on mobile. Use the same brand and tokens, avoid feature claims or promotional paragraphs, and show account help where it can resolve a login problem.

Use a white main workspace, a pale neutral sidebar and soft light-gray result cards. Resource tables remain white. Keep navigation quiet with a selected pill, a simple brand and account bar, and one vivid primary action. Keep real tabs and filters at the top of the same content panel when those features exist; never invent tabs, data or metrics to fill the layout. Record only Evia-owned design decisions. Do not include names, screenshots or branding from supplied interface references in source, documentation, fixtures, assets or commit messages; retain required dependency attribution and license notices.

## Interaction and evidence

Each feature must handle loading, empty, error, unauthorized, stale and successful states. Keep labels visible, associate field errors with inputs, give busy controls stable accessible names and expose their busy state. Support keyboard navigation, visible focus, safe confirmation defaults, focus restoration after dialogs and reduced motion. Destructive actions explain their concrete effect. Failed requests, unsaved edits and revision conflicts must remain recoverable.

Each configuration dialog validates its own section when completed; an unrelated incomplete section must not trap the user. Running a query still validates the complete request and reveals or focuses the relevant editor on failure. Editing a setting or choosing a preset must not silently submit an expensive query. Tabs must control the actual visible content, never an empty focusable panel next to it. When controls are added or removed, preserve a useful keyboard position without taking focus from a newer interaction.

Tables may scroll horizontally within their surface on narrow screens; the page itself must not overflow. Dialogs, labels, footer actions and error feedback must fit a narrow viewport. Long resource names and identifiers must wrap or truncate with a way to inspect or copy the full value.

Use charts when comparisons, trends or transitions benefit from them. Show units, time range, timezone and precision; provide an accessible textual or table equivalent. Never show mock metrics as real data or use charts to disguise missing functionality.

### Component interaction-state matrix

| Component | States and behavior to verify |
| --- | --- |
| Button | Default, hover, pointer/Space press, keyboard focus, disabled and loading; native Enter/Space activation occurs once, default button type does not submit, submit buttons respect form validation and event cancellation |
| Text input / textarea | Label focus, typing, blur, selection, read-only, disabled, required and invalid; focused/invalid appearance remains meaningful while hovered, errors are associated and announced, read-only text remains selectable |
| Select / checkbox | Native pointer and keyboard changes, label activation, controlled value, disabled and invalid states; visible focus and change callbacks preserve native semantics |
| Modal | Open/close, topmost Escape, nested confirmations, backdrop, busy state, initial focus and safe return target; the background remains inert and browser chrome can retain its native keyboard behavior |
| Table / pagination | Refresh preserves readable rows and identifies busy state; keyboard can reach actions and horizontal overflow, page boundaries disable unavailable navigation |
| Copy / notification | Pending, success, error, changed value and unmount; late results cannot announce success for a new value, focused/hovered messages remain usable, dismissal preserves a meaningful focus target |

Persistent toggles need a visible selected state after pointer release, separate from transient press and keyboard focus. Notification queue limits must protect hovered and focused messages as well as pausing their expiry. Dialog restoration must yield to a newer pointer, keyboard, window or dialog focus owner; if the original trigger was removed, use a connected contextual fallback. Verify these transitions with browser actions, including narrow dialogs, overlapping confirmations and burst notifications.

Forward native event and ref props without swallowing caller handlers. Prefer browser activation and form semantics over parallel key handlers that can submit twice. Honor `preventDefault` and disabled behavior. Async focus recovery must verify that the component previously owned focus and that no newer interaction, dialog or navigation has established another target; completion must not steal focus from the user.

State CSS needs deliberate precedence, including combinations such as invalid + focused + hovered. A hover selector must not erase error or focus feedback. Test visible keyboard focus against adjacent colors and within scroll containers; preserve reduced motion and native read-only selection. Shared styles and behavior belong in the design system, with business validation and request recovery in page code.

Use isolated browser fixtures to exercise shared controls and lifecycle cases absent from business pages. Fixture counters, deferred browser APIs and state toggles are test inputs, never product routes or substitute service responses. Verify current pages separately against real APIs. Passing a CSS selector assertion alone does not establish interaction correctness.

## Performance evidence

Use shared static CSS and theme variables. Split business routes and load chart code on demand. Keep server pagination and bounded query results; introduce list virtualization only when measured row counts and interaction costs justify it. Keep form input state local and profile render work before adding memoization.

Measure the existing production build before migration and the resulting build afterwards with the same tool versions, build mode and routes. Record raw and gzip JavaScript/CSS sizes for initial routes and any lazy chunks, plus browser loading and interaction measurements with stated viewport, device/network settings and sample count. Custom components alone do not establish a speed improvement. Investigate regressions and keep keyboard access, complete states and error recovery intact; changing a bundle warning threshold is not a performance fix.

Complete backend behavior and tests before frontend implementation. Run type checking and a production build, then verify successful and failed operations against the real backend in a browser. Exercise keyboard focus and narrow layouts, assert no uncaught browser exceptions, and visually inspect fresh desktop and mobile screenshots before declaring the interface complete.
