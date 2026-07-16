# Codex prompt 05 — runtime adapter matrix

## Goal

Implement and certify Codex, Claude Code, OpenCode and Hermes against one runtime conformance kit while preserving native capabilities through explicit negotiation.

## Context

Read ADR-004, ADR-021–029, specification Section 9, the pinned release inputs and the existing first real adapter. Research only current primary upstream documentation and pin exact versions/schemas used.

## Work

1. Complete the shared conformance suite before adding adapter-specific features.
2. Implement one native southbound adapter per runtime family using the confirmed interfaces.
3. Capture upstream schemas, recorded event fixtures, version/digest, provider profile and capability results.
4. Map lifecycle, events, questions, approvals, tools/processes, files/changes, artifacts, usage, interrupt/cancel and resume without inventing parity.
5. Required capability loss blocks publication; optional loss creates an explicit degraded mode.
6. Keep Hermes profile state, messaging, skills, schedules, memory, Kanban and ephemeral delegation semantics distinct from coding sessions and OpenShell sandbox identity.
7. Test prior-matrix migration/rollback and schema drift alarms.
8. Produce a human- and machine-readable capability matrix.

## Constraints

- No ACP southbound adapter unless a new confirmed ADR changes the decision.
- No multiple production integrations per runtime.
- No raw provider credentials in a sandbox process.
- No harness permission mode can widen Cedar/OpenShell.
- Do not flatten Hermes profiles into sandbox or delegated-agent concepts.

## Done when

- All four families pass required conformance for the initial release slice.
- Differences and degradations are explicit in API/UI and release evidence.
- Upgrade, canary, migration and rollback paths are tested.
- Five review passes cover factual and contract accuracy, scope and instruction precedence, security and isolation, failure recovery and operability, then maintainability and regression. Retain only improvements.
- Return the certification matrix, failures, limitations and release blockers.
