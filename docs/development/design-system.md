# Design-system development

DataGround owns its semantic tokens, interaction contracts, status vocabulary, and product patterns. React Aria Components supplies accessible behavior for the initial React primitives, but its API is not the DataGround public component API. The workbench consumes `@dataground/ui` rather than importing that dependency directly.

## Package boundaries

`@dataground/tokens` owns DTCG source files, deterministic generation, four themes, three density modes, CSS variables, and type-safe token names. `@dataground/ui` owns framework-level accessible primitives and their styles. `@dataground/patterns` owns product-specific compositions with explicit state and callback contracts; its first consumers are agent-service creation, revision drafting, invocation approval, event timeline, artifact metadata, invocation status, and invocation composer workflows. Authorization, network requests, resource commands, and product state do not belong in these packages.

The Workbench service dashboard reads bounded authoritative pages for the active isolation domain,
rejects malformed, duplicate, or cross-domain resources, and keeps loading, empty, partial-error,
and retry states distinct. A newly created service is inserted into the current view immediately;
refresh remains authoritative, and continuation never widens the active scope.

The current `Button`, `StatusBadge`, and controlled `TextField` primitives establish interaction, status, and accessible input conventions. `AgentServiceCreate` establishes a governed product identity without asking for runtime, provider, or sandbox configuration; the Workbench owns authority, exact isolation scope, request normalization, response validation, and same-key recovery. `ServiceRevisionDraft` binds that identity to one provider-neutral runtime profile, capability set, and optional input and output schemas while keeping publication and alias assignment separate; the Workbench validates exact scope and definition, retains the same command after uncertain acknowledgement, and presents accepted state only as an unpublished draft. `ApprovalRequest` composes the primitives without loading data or deciding authorization: the Workbench supplies the provider-neutral approval, authority result, recovery state, and commands. `EventTimeline` renders already validated, ordered, and bounded provider-neutral event envelopes. The Workbench owns finite replay requests, scope validation, cursor confirmation, deduplication, and recovery; the pattern never opens a stream or interprets provider-native payloads. `ArtifactCard` renders governed descriptor state, sensitivity, integrity, and provenance without implying that artifact content is available; the Workbench owns the exact scoped metadata read and stale-response recovery. `InvocationStatus` keeps invocation and durable-operation observations distinct; the Workbench requires both exact-scope reads before enabling a new cancellation, confirms the irreversible command separately, retains one idempotency key when the cancellation outcome is uncertain, and exposes only a bounded set of authoritative artifact references for governed metadata inspection without loading content. `InvocationComposer` renders only a normalized, closed string-field schema and explicit service scope. The Workbench rejects unsupported schema semantics, decides authority, validates UTF-8 payload bounds, submits the scoped command, and retains the exact request and idempotency key whenever acknowledgement is uncertain; the pattern never exposes provider or runtime controls.

## Token contract

Source files under `packages/tokens/src` target the [DTCG Format Module 2025.10](https://www.designtokens.org/tr/2025.10/format/) and [Color Module 2025.10](https://www.designtokens.org/tr/2025.10/color/). The repository compiler intentionally supports only the token types it emits: color, dimension, duration, font family, font weight, number, and whole-token aliases. Unsupported types fail generation instead of being guessed.

Foundation tokens describe palette, spacing, typography, radii, borders, motion, and layering. Theme files provide the same semantic color paths for night, light, high-contrast dark, and high-contrast light. Density files provide the same sizing paths for compact, standard, and comfortable modes. A missing path, unresolved alias, circular alias, type mismatch, malformed color, insufficient normal-text contrast for a declared semantic pair, or stale generated artifact fails verification.

Edit source token files, then regenerate and validate them:

```shell
pnpm tokens:generate
pnpm tokens:check
pnpm --filter @dataground/tokens test
```

Generated files under `packages/tokens/src/generated` are committed so consumers do not need a token compiler during development. Do not edit them directly.

Themes and densities are selected with `data-theme` and `data-density`. Root selection configures the application; scoped selection supports isolated documentation and migration surfaces. With no explicit theme, the system follows the light color-scheme preference and otherwise uses night. Reduced-motion media preferences replace non-essential transition durations with zero.

## Component contract

Shared components wrap accessible headless behavior behind DataGround-owned props, semantic styling, and tests. Follow the relevant [WAI-ARIA Authoring Practices](https://www.w3.org/WAI/ARIA/apg/) and the upstream [React Aria component documentation](https://react-aria.adobe.com/) while keeping upstream-specific state and types from leaking unnecessarily into product modules.

Every stable component needs a named owner, intentional variants, controlled-state behavior, keyboard and focus expectations, screen-reader behavior, Storybook stories, focused tests, and migration notes for breaking changes. Color is never the only status signal. Stable component accessibility violations are configured as Storybook test errors through the official [accessibility addon](https://storybook.js.org/docs/writing-tests/accessibility-testing).

Run Storybook for component work:

```shell
pnpm dev:design-system
pnpm --filter @dataground/ui test:stories
pnpm --filter @dataground/ui storybook:build
```

Storybook toolbar controls exercise every component across four themes and three densities. Meaningful interactive stories use `play` assertions so examples remain executable contracts. The story test command runs the stories in pinned headless Chromium and treats configured accessibility violations as errors. CI installs that browser explicitly. Browser tests and production Storybook builds are part of `pnpm verify`, which prevents broken stories, accessibility regressions, or undocumented imports from landing.

## Change discipline

Add a foundation token only when multiple semantic roles need the value. Add a semantic token when the role is stable across components and themes. Add a component token only when semantic roles cannot express a verified need. One-off product values remain local until evidence supports promotion.

Do not add a primitive only to complete an inventory. Introduce it with a real consumer, its contract surface, interaction evidence, and tests. Product patterns must preserve requested versus observed state, typed events, explainable authority, provenance, recovery, and unknown states from the platform specification.
