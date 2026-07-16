# Design-system development

DataGround owns its semantic tokens, interaction contracts, status vocabulary, and product patterns. React Aria Components supplies accessible behavior for the initial React primitives, but its API is not the DataGround public component API. The workbench consumes `@dataground/ui` rather than importing that dependency directly.

## Package boundaries

`@dataground/tokens` owns DTCG source files, deterministic generation, four themes, three density modes, CSS variables, and type-safe token names. `@dataground/ui` owns framework-level accessible primitives and their styles. Product-specific compositions will belong in `@dataground/patterns` only when a real workflow needs them. Authorization, network requests, resource commands, and product state do not belong in these packages.

The current `Button` and `StatusBadge` are the first verified consumers of this boundary. They establish interaction and status conventions without prematurely implementing the full inventory.

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
