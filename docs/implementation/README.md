# DataGround implementation starter

This directory turns the normative architecture into an implementation starting point. It does not replace the [architecture decision register](../architecture/decision-register.md) or [system specification](../architecture/system-specification.md).

## Start here

1. Read [source-of-truth.md](source-of-truth.md) for authority and conflict rules.
2. Resolve the repository-bootstrap items in [build-readiness.md](build-readiness.md).
3. Use [first-vertical-slice.md](first-vertical-slice.md) as the initial product plan.
4. Evaluate the proposed shape in [repository-and-contract-blueprint.md](repository-and-contract-blueprint.md) against verified repository facts.
5. Review [design-system-foundation.md](design-system-foundation.md) before creating production UI components.
6. Apply [verification-and-release-gates.md](verification-and-release-gates.md) at every checkpoint.
7. Use one task from [`prompts/`](prompts/) at a time, starting with repository discovery and bootstrap.

## Guidance status

[proposed-decisions.md](proposed-decisions.md) contains recommendations that still need confirmation or evidence. [references.md](references.md) provides the focused reference set for bootstrap and frontend work. [PLANS.md.template](PLANS.md.template) is the repository execution-plan template for complex changes.

Implementation can begin on the contract core, deterministic reference runtime, durable resource state, event journal, transactional outbox, audit vocabulary, design tokens, accessible primitives, and conformance harnesses. Production policy materialization remains blocked on Rosetta's unpublished service contract. Until that contract is frozen, Rosetta-dependent behavior must stay fail-closed and use only explicit non-production fixtures.
