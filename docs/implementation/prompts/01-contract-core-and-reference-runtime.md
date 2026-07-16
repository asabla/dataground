# Codex prompt 01 — contract core and deterministic reference runtime

## Goal

Implement the versioned DataGround agent-service contract core and a deterministic fake runtime that exercises success and failure semantics without OpenShell or provider dependencies.

## Context

Read the normative specification and ADRs, then `docs/implementation/first-vertical-slice.md`, `docs/implementation/repository-and-contract-blueprint.md` and `docs/implementation/verification-and-release-gates.md`. Inspect existing contracts before adding new ones.

## Work

1. Define canonical resource metadata, error envelope, operation, agent service, immutable revision, alias, deployment, invocation, event envelope, artifact descriptor, runtime capability manifest and release manifest.
2. Use the confirmed schema toolchain; if it is not confirmed, stop at a proposal/spike instead of creating competing sources of truth.
3. Add valid and invalid examples plus compatibility/breaking-change checks.
4. Implement a deterministic reference runtime with fixtures for text, tool/process activity, question, approval, artifact, usage, cancellation, retryable failure, terminal failure, duplicate events, out-of-order delivery and unknown optional events.
5. Implement API behavior only as needed to create/publish a revision, assign an alias, invoke, stream/replay events, cancel and retrieve the result/artifacts using the fake runtime.
6. Ensure large/sensitive event payloads use artifact references and unknown event fields/types degrade safely.

## Constraints

- Public IDs do not reveal database, gateway, sandbox or upstream IDs.
- Every resource is isolation-domain scoped.
- Every mutation accepts an idempotency key.
- Provider-specific data is namespaced and optional.
- Do not add OpenAI-compatible endpoints.
- Do not integrate a real runtime or OpenShell in this task.

## Done when

- Generated clients/types are reproducible.
- Contract tests cover all fixtures and errors.
- REST commands plus SSE replay work end to end against the fake runtime.
- Duplicate delivery and reconnect are tested.
- API documentation contains examples and compatibility rules.
- Five review passes cover factual and contract accuracy, scope and instruction precedence, security and isolation, failure recovery and operability, then maintainability and regression. Retain only improvements.
- Return evidence, remaining gaps and whether prompt `02` is unblocked.
