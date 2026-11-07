# Console design conventions

Create shared tokens and components in packages/design-system, then build business pages in apps/console. The design system owns color roles, spacing, typography, focus states, table patterns, form controls, dialogs, feedback and page layout.

Use an eight-pixel spacing rhythm with four-pixel adjustments. Establish a clear page title, scope selector, primary action and content hierarchy. Put project/environment context where it remains visible during changes. Give dense data a readable table, optional details and bounded pagination rather than turning every value into a card.

Each feature must handle loading, empty, error, unauthorized, stale and successful states. Keep labels visible, associate field errors with inputs, support keyboard navigation, restore focus after dialogs and respect reduced motion. Destructive actions explain their concrete effect. Unsaved edits and revision conflicts remain recoverable.

Use charts when comparisons, trends or transitions benefit from them. Show units, time range, timezone and precision; provide an accessible textual/table equivalent. Never show mock metrics as real data or use charts to disguise missing functionality.

Concise means clear hierarchy and focused wording. It does not mean omitting necessary filters, status, explanations or error recovery. Complete backend behavior and tests before frontend implementation; verify the completed module in the browser before starting another module.
