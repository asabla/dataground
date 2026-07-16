# Codex prompt 04 — workbench and design system

## Goal

Create the production design-system foundation and the agent-service workbench flow using the same schemas and deterministic event fixtures as the backend.

## Context

Confirm the frontend stack decision and inspect any implementation that exists before changing architecture. Read `docs/implementation/design-system-foundation.md`, the frontend contract in the specification, ADR-003/005/007/009/010/012, and `docs/implementation/verification-and-release-gates.md`.

## Work

1. Document the actual frontend framework, state/data layers, routing, Jupyter integration, tokens, component system, tests and hosting.
2. Confirm or amend proposed decision P-003. Do not introduce React if the existing workbench is not React without an approved migration decision.
3. Implement token source, light/dark/high-contrast themes and reduced-motion behavior.
4. Add accessible primitive components needed by the slice and a component workbench/testing surface.
5. Build scope navigation, service/revision header, publish flow, schema-driven invocation composer, event timeline, question/approval, operation state, cancellation, reconnect/replay, artifact/provenance and policy/capability explanation.
6. Use deterministic backend fixtures in stories and integration tests.
7. Show desired vs observed state, controller vs observer and unknown/degraded states explicitly.
8. Preserve the restrained visual direction defined in `docs/implementation/design-system-foundation.md`.

## Constraints

- WCAG 2.2 AA is the complete-journey target.
- Color is never the only status indicator.
- Live announcements are bounded; do not announce token/log streams continuously.
- No one-off colors/status meanings or provider-specific public nouns.
- Approval UI cannot imply authority beyond the returned policy/enforcement state.
- Do not build notebook/lakehouse breadth in this task.

## Done when

- Stable stories pass automated accessibility and interaction checks.
- Critical journeys pass manual keyboard, focus, zoom/reflow, reduced-motion and one recorded screen-reader/browser test.
- Dark/high-contrast, narrow layout, RTL/localization expansion, loading, empty, error and unknown states are covered.
- Reconnect/replay does not duplicate events or misleading announcements.
- Five review passes cover factual and contract accuracy, scope and instruction precedence, security and isolation, failure recovery and operability, then maintainability and regression. Retain only improvements.
- Return screenshots/test evidence, changed components/tokens and unresolved design decisions.
