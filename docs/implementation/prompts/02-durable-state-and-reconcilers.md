# Codex prompt 02 — durable state and reconcilers

## Goal

Make service publication and invocation durable through explicit PostgreSQL state machines, transactional outbox delivery and replaceable workers.

## Context

Read ADR-038, specification Sections 11, 15, 19 and 20, and `docs/implementation/verification-and-release-gates.md`. Use the contract core and fake runtime already implemented.

## Work

1. Model finite publication and invocation state machines with commands, preconditions, terminal states, retry classifications, deadlines, cancellation and repair.
2. Persist desired/observed state, generation, state-machine version, operation/attempt, lease/fencing, `due_at`, error classification and terminal result.
3. Commit accepted commands/transitions and outbox records atomically.
4. Make command, callback and provider observation processing idempotent.
5. Use deterministic external operation IDs and observe ambiguous outcomes before repeating effects.
6. Add fair claiming so one scope/resource cannot monopolize workers.
7. Add audit and OpenTelemetry correlation without payload leakage.
8. Add migrations, mixed-version behavior and rollback/forward-repair documentation.
9. Build failure-injection tests at every durable transition and fake external effect.

## Constraints

- No generic workflow table, user-authored workflow DSL, process-local durable timer or in-memory exclusive state.
- No Temporal, Argo or Airflow dependency.
- Repair operations are authorized, audited and repeatable.
- Preserve the public operation/event contracts.

## Done when

- Killing API/worker processes cannot lose accepted commands or create duplicate external effects.
- Expired leases recover safely and stale owners cannot commit.
- Cancellation reaches a stable state and prevents new effects.
- Database migrations and repair runbooks are tested.
- Five review passes cover factual and contract accuracy, scope and instruction precedence, security and isolation, failure recovery and operability, then maintainability and regression. Retain only improvements.
- Return evidence and whether prompt `03` is safe.
