# DataGround design-system foundation

## Recommendation

Build an owned, token-driven DataGround system on top of accessible headless primitives. If the confirmed workbench stack is React, use React Aria Components as the primitive layer, CSS custom properties generated from Design Tokens Community Group-format JSON, and Storybook as the component contract and test surface.

Do not make a copied component catalog or utility-class framework the design-system source of truth. They may assist implementation, but DataGround should own its semantic tokens, interaction patterns, component APIs, status language, and product-specific compositions.

If a non-React workbench is selected, retain the tokens, accessibility target, component specifications, and visual vocabulary; select an equivalent primitive library for that framework instead of introducing React solely for the design system.

## Product experience principles

1. **Scope is always visible.** Installation, organization/team, workspace, service/revision and active principal are never implicit during consequential actions.
2. **Desired and observed state are distinct.** Show requested state, current state, last observation, active operation, and recovery options without pretending reconciliation is instantaneous.
3. **Events precede chat bubbles.** Text, tools, processes, files, questions, approvals, usage, schedules, warnings and lifecycle changes share one typed timeline.
4. **Authority is explainable.** A grant, denial or approval request shows the relevant scope, capability, policy revision, enforcement availability, expiry and consequences.
5. **No hidden side effects.** Mutations state target, scope and reversibility. Destructive or externally visible actions require clear confirmation or preauthorization evidence.
6. **Provenance travels with outputs.** Artifacts, datasets, revisions and generated changes expose source runtime, inputs, policy, timestamps, version and lineage.
7. **Dense, not cramped.** The platform serves technical operators; support information density with hierarchy, resizable regions, keyboard operation and progressive detail.
8. **Failure is a first-class state.** Preserve partial results, safe diagnostics, correlation IDs, retryability and repair paths.
9. **Color never carries meaning alone.** Every state has text, icon/shape and accessible announcement.
10. **Infrastructure stays behind the product boundary.** UI concepts are DataGround resources, never raw pods, forwarded ports or upstream runtime protocol objects.

## Design-system packages

| Package | Responsibility |
| --- | --- |
| `@dataground/tokens` | Source token JSON, themes, generated CSS variables and type-safe names |
| `@dataground/ui` | Accessible primitives and common components |
| `@dataground/icons` | Curated icons with semantic naming and accessible usage rules |
| `@dataground/patterns` | Product compositions such as operation status, event timeline and approval panel |
| `@dataground/visualization` | Architecture, lineage, capacity and telemetry visual grammar |

Keep product data fetching and permissions outside `@dataground/ui`. Patterns accept explicit state and callbacks; application modules own authorization and commands.

## Token model

Use three layers:

1. **Foundation:** palette, typography, spacing, size, radius, border, shadow, motion and z-index.
2. **Semantic:** canvas, surface, text, border, focus, action, selection, critical, warning, success, info, neutral and data-series roles.
3. **Component:** only where a component cannot be expressed cleanly with semantic roles.

Required themes:

- light;
- dark;
- high-contrast light;
- high-contrast dark;
- reduced motion through media preference rather than a separate visual theme.

Token names describe purpose, not a literal color: `color.text.muted`, `color.action.primary.background`, `color.status.critical.border`. Runtime state names do not become colors directly; a mapping layer assigns semantic presentation.

The DTCG format is an interoperability target, not yet a W3C standard. Pin the supported format revision and validate token files in CI.

## Typography and density

- Use a platform-native UI sans-serif stack first; introduce a branded font only with a performance, licensing and internationalization case.
- Use a dedicated monospace stack for code, IDs, logs and structured values.
- Define compact, standard and comfortable density modes through tokens. Compact mode must not violate WCAG target-size requirements for pointer-only controls.
- Use tabular numerals for metrics, usage and timestamps.
- Keep body text readable; dense tables may reduce size only when zoom, reflow and keyboard navigation remain usable.

## Initial component inventory

### Foundations

Button, icon button, link, text field, number field, search field, select, combo box, checkbox, radio group, switch, tabs, disclosure, tooltip, popover, dialog, menu, toast, progress, skeleton, badge, tag, avatar, separator, scroll area and resizable region.

### Data and workbench

Data table, tree, resource list, virtual collection, filter builder, query bar, code block, diff viewer shell, JSON/schema form shell, split pane, command palette, empty state and error boundary.

### DataGround patterns

- `ScopeBreadcrumb` and `ScopeSwitcher`
- `ResourceHeader` with generation/revision/provenance
- `OperationStatus` with desired/observed state
- `EventTimeline` and typed event cards
- `ApprovalRequest` and `QuestionRequest`
- `CapabilitySummary` and `PolicyExplanation`
- `ArtifactCard` and provenance drawer
- `RevisionDiff`, `RolloutStatus` and `MigrationStatus`
- `GatewayPlacementExplanation`
- `UsageMeter` with measured/unknown distinction
- `AuditTrail`
- `ControllerLease` for controller/observer coordination

## Status vocabulary

Backend domain enums remain authoritative. The UI maps them into a small presentation vocabulary:

| Presentation state | Meaning |
| --- | --- |
| Neutral | Draft, inactive, unknown or informational |
| Pending | Accepted but not yet active |
| Active | Making progress or ready for interaction |
| Waiting | Intentionally blocked on input, approval, dependency or schedule |
| Succeeded | Terminal and completed as requested |
| Warning | Completed or active with declared degradation/risk |
| Failed | Terminal failure or action required |
| Cancelling | Cancellation accepted; effects are still reconciling |

Never map an unknown backend state to success. Show the raw safe state label when a newer server state reaches an older client.

## Accessibility and interaction contract

- WCAG 2.2 AA is the release target for complete user journeys, not only isolated components.
- All functionality is keyboard-operable with visible, unobscured focus.
- Complex widgets follow WAI-ARIA Authoring Practices and include screen-reader announcements for async state.
- Live event streams use bounded, user-controllable announcements; do not announce every token or log line.
- Drag-and-drop has a non-drag alternative.
- Resize, zoom, reflow, RTL, localization and long translated strings are tested.
- Motion respects `prefers-reduced-motion`; no essential state is conveyed only by animation.
- Automated checks fail CI for new violations, but manual keyboard and screen-reader testing remains required.

## Workbench information architecture

Primary navigation should be resource-oriented:

- Home/operations
- Workspaces
- Agent services
- Interactions and invocations
- Notebooks
- Data
- Jobs
- Environments
- Gateways and capacity
- Policies and access
- Artifacts
- Audit
- Administration

Use contextual secondary navigation within a resource. Do not expose every platform module as a top-level destination in the initial MVP.

## Visual direction

Use a restrained, operational direction: neutral surfaces, one primary accent, fine borders, modest radii, compact typography and semantic visualization colors.

- neutral canvas and surfaces dominate;
- accent color indicates selection or primary action, not decoration;
- status colors are reserved for status;
- elevation is rare and communicates layering;
- diagrams and timelines use the same semantic token vocabulary;
- high-density views prefer alignment, spacing and typography over card proliferation.

## Governance and release

- Every component has an owner, status (`experimental`, `stable`, `deprecated`), accessibility notes, allowed variants and tests.
- Stable component breaking changes require migration notes and a deprecation window.
- Storybook stories cover states, themes, density, localization, keyboard paths, reduced motion and narrow/wide layouts.
- Screenshot testing uses a controlled browser/font environment; it supplements interaction and accessibility tests.
- Product teams cannot add one-off colors, spacing or status semantics without a token or documented exception.
- Review design-system adoption after the first three real workflows; avoid prematurely building unused components.

## First design deliverables

1. Token source plus four themes.
2. Storybook with accessibility checks enabled as errors for stable components.
3. Button, fields, tabs, dialog, menu, tooltip, toast and progress primitives.
4. Workbench shell, scope breadcrumb, resource header and operation status.
5. Event timeline using deterministic runtime fixtures.
6. Approval/question patterns and policy explanation.
7. Accessible invocation composer generated from the first input schema.
8. Manual test scripts for keyboard, screen reader, zoom/reflow and reduced motion.
