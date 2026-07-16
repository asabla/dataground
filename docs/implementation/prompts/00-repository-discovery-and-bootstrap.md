# Codex prompt 00 — repository discovery and bootstrap

## Goal

Turn the DataGround architecture repository into a verified, executable monorepo foundation for the governed agent-service vertical slice. Introduce only the agreed skeleton and contract/test infrastructure after reporting unresolved inputs.

## Context

Read, in order:

1. the repository's applicable `AGENTS.md` files;
2. `docs/architecture/decision-register.md`;
3. `docs/architecture/system-specification.md`;
4. the complete `docs/implementation/` starter;
5. existing frontend, backend, deployment and CI files.

ADR-003 makes agent services the first vertical slice. ADR-019 makes Rosetta external. Do not follow older phase text that contradicts those decisions.

## Work

1. Inventory the repository, existing services, frontend framework/protocol, package managers, build/test commands, CI, deployment files, schemas and generated code.
2. Verify commands by running safe read/build/test checks; do not put invented commands in `AGENTS.md`.
3. Produce a gap matrix against `docs/implementation/build-readiness.md` with `known`, `missing`, `conflicting`, `blocked` and `proposed` classifications.
4. Propose the smallest monorepo layout that preserves existing code and ADR-011. Do not split into microservices without a real boundary.
5. Establish formatting, lint/type checks, unit test, schema validation, generation drift and CI entry points.
6. Update `AGENTS.md` through `.agents/skills/update-agents-md/SKILL.md` when verified repository facts change. Use `docs/implementation/PLANS.md.template` for the execution plan.
7. Create a versioned release-manifest schema skeleton without pinning fake versions.
8. Keep unrelated user changes intact.

## Constraints

- Do not implement product features in this task.
- Do not build a Cedar-to-OpenShell compiler.
- Do not select infrastructure products or languages silently; use confirmed choices or record a proposed decision with evidence needed.
- Do not select or scaffold a frontend framework without a confirmed decision or an explicitly bounded evidence spike.
- Do not add dependencies merely to make the proposed directory tree exact.

## Done when

- A clean checkout has documented, verified bootstrap and validation commands.
- The actual repository/frontend facts and remaining P0 decisions are explicit.
- CI and local validation can run a minimal deterministic test.
- Root agent guidance contains no fiction.
- A five-pass review covers contract fidelity, security boundaries, failure/recovery implications, maintainability and final regression.
- Return changed files, commands/results, decisions needed and the next prompt that is safe to run.
