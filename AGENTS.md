# DataGround agent guidance

DataGround is security-sensitive platform infrastructure for data workloads, notebooks, coding agents, and persistent assistants. Correctness, fail-closed behavior, stable contracts, maintainability, and operational clarity take priority over expedient changes.

## Current repository state

This repository contains an executable foundation: control-plane entry points are under `cmd`, internal Go packages are under `internal`, the React workbench is under `apps/workbench`, shared design tokens and accessible primitives are under `packages/tokens` and `packages/ui`, and canonical public contracts and generated types are under `contracts` and `apps/workbench/src/contracts`.

The normative sources are `docs/architecture/decision-register.md` and `docs/architecture/system-specification.md`, in that order when they conflict. Implementation prerequisites, proposed choices, verification gates, and handoff prompts under `docs/implementation/` remain subordinate to those sources.

Use `README.md` for concise repository status, `docs/development/README.md` for layout and commands, `docs/development/reference-runtime.md` for reference-runtime behavior, and `docs/development/openshell-local.md` for OpenShell integration and certification limits. Keep detailed implementation inventories in those focused documents rather than duplicating them here.

Do not invent repository paths, commands, supported behavior, dependency choices, production guarantees, or certification claims. Introduce them only with the implementation and evidence that make them true, and update this guidance in the same change when they become durable repository facts.

The deterministic reference runtime supports loopback-only process-local operation and PostgreSQL-backed durable development. OpenShell, enforcement, admission, native runtime, artifact, recovery, and credential-evidence components remain internal, opt-in, development-scoped, or unwired unless the canonical documentation and verified tests explicitly say otherwise. Strict injected HTTP authentication and action-authorization boundaries now bind API requests, actor attribution, isolation-domain membership, idempotency replay, and path-derived Cedar decisions to one validated principal. Durable API mode records completed authentication attempts and action evaluations in append-only PostgreSQL audit boundaries with shared request correlation and immutable authorization-policy provenance, and an audited internal operator command can export frozen isolation-scoped pages from both decision streams only after recording an append-only receipt; process-local mode does not claim durable audit. The command composes only a loopback-bound static development identity and Cedar policy. An audited operator command can install one immutable Cedar invocation policy for an exact service revision, and durable authorization can resolve that bundle from PostgreSQL without wildcard or latest-revision fallback. An explicit loopback-only governed development worker can compose exact-revision policy resolution, append-only decision audit, OpenShell admission, claim-fenced Codex runtime execution, cancellation, and artifact finalization for one configured isolation domain while retaining deterministic reference behavior elsewhere. Reference mode remains the default. An internal provider-neutral OIDC boundary can verify compact JWT access tokens against an immutable deployment-owned JWKS profile, atomically replace complete versioned keyset snapshots, independently pin issuer and audience values, and resolve issuer/subject through audited, append-only PostgreSQL registrations for exact human or external-service domain memberships without trusting token-carried scope. An internal durable OIDC/DPoP assembly now owns the reloadable verifier, identity resolution, replay ledger, pinned-origin request binding, fail-closed pre-authentication admission, both durable audit layers, a bounded single-owner keyset refresh lifecycle with fail-closed readiness, and a strict atomic-file publication source, and a PostgreSQL-coordinated layered admission policy, but remains unwired from the executable; provider DPoP issuance, reviewed TLS ingress deployment, optional nonce policy, OIDC discovery, deployment key generation and publication orchestration, deployment rate-limit policy rollout and measured capacity, executable startup configuration, policy authoring and distribution, entity loading, audit transport, acknowledgement, access and retention policy, provider credential mediation, default or public governed execution, production backend selection, production artifact storage, and production infrastructure remain unimplemented. Do not claim live-runtime or production certification beyond evidence incorporated in the canonical documentation.

Complete the governed agent-service vertical slice before integrating broad notebook, lakehouse, job, or compatibility surfaces.

## Architecture boundaries

Public clients use DataGround APIs and resource identities. They must never receive OpenShell gateway addresses, sandbox ports, Kubernetes credentials, root storage credentials, or native harness endpoints.

Cedar expresses authorization intent. Rosetta is the Cedar-to-OpenShell materialization boundary; do not add a fallback compiler inside DataGround. An unavailable or unrepresentable translation fails closed. OpenShell provides the sandbox execution and enforcement boundary, while DataGround owns product identity, authorization, durable state, placement, lifecycle, audit, and public contracts.

Provider credentials must never be readable by notebook or harness processes. Use OpenShell-mediated credentials or an explicitly designed credential-holding bridge. OpenShell provider-profile selection must resolve through the immutable deployment-owned registry; malformed, unregistered, or fallback profiles fail closed before provider access. Treat raw provider-secret visibility as a configuration and release failure.

Durable lifecycle state belongs in PostgreSQL-backed, finite resource state machines. Workers are replaceable, state transitions and outbox records commit atomically, timers and retries are durable, and ambiguous external effects are observed before repetition. Process-local state is permitted only in the explicitly documented reference implementation and must not acquire production guarantees. Do not introduce a general workflow engine or generic workflow abstraction without a new architectural decision supported by measured need.

Every resource, authorization decision, cache key, queue item, storage prefix, event, metric, and audit record is isolation-domain scoped. Trusted installations may simplify tenant concepts in the user interface, but implementation must not weaken scope separation.

Native runtime protocols remain internal. Codex, Claude Code, OpenCode, and Hermes integrate through one platform contract with explicit capability negotiation. Preserve Hermes profile, delegation, Kanban, memory, skill, schedule, and messaging boundaries rather than flattening them into generic sessions. The platform-native service and event APIs precede any OpenAI-compatible facade.

## Engineering approach

Prefer focused changes that solve the underlying problem and leave clear extension points. Avoid speculative abstractions, premature microservices, duplicated policy or lifecycle semantics, and compatibility layers without a concrete consumer. A deployable boundary must have a distinct trust, scaling, failure, runtime, or release reason.

Use prose by default in documentation, comments, commit messages, and pull request descriptions. Use lists when they communicate a real sequence or exact set more clearly than prose. Comments should explain invariants, tradeoffs, or non-obvious reasoning instead of restating code.

Write pull request descriptions as direct, concise prose for human reviewers. Explain why the change is needed and call out pivotal decisions or reviewer caveats. Do not narrate the diff, include iteration scores, or add template headings that merely label obvious paragraphs. Let configured checks report routine verification; state limitations plainly when checks do not yet exist.

Follow [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/) for commit headers: `<type>[optional scope][!]: <description>`. Use a stable scope only when it adds useful context, write concise lower-case imperative descriptions without a trailing period, and document breaking changes in a `BREAKING CHANGE:` footer.

Do not provide time estimates. Describe scope, dependencies, risks, decisions, and verification evidence. Prefer durable, long-term solutions when a shortcut would create another contract, migration, security exception, or maintenance burden.

## Contracts, security, and observability

Treat API schemas, event envelopes, error codes, diagnostic codes, resource states, database migrations, audit fields, artifact descriptors, and release manifests as compatibility surfaces. Version material semantic changes and keep schemas, generated clients, examples, tests, and documentation synchronized.

Mutating commands are idempotent. External operations use deterministic identifiers. Public errors contain stable codes, safe messages, correlation identifiers, and retryability without leaking upstream payloads or secrets. Large or sensitive event content becomes a governed artifact reference rather than an unbounded inline payload.

Authorize at entry and again before consequential external effects. Test denials, malformed inputs, duplicates, cancellation, timeouts, lost acknowledgements, restarts, partial effects, stale leases, cross-domain collisions, and deletion—not only successful behavior.

Logs, traces, metrics, and audit records share correlation fields but have different retention and sensitivity contracts. Do not log tokens, prompts, notebook or artifact contents, provider responses, policy entities, or generated enforcement artifacts by default.

## Frontend expectations

Use semantic design tokens and shared accessible primitives before creating local variants. WCAG 2.2 AA is the complete-journey target. All functions must be keyboard-operable with visible focus, and color must never carry status alone.

Token source files conform to the pinned DTCG 2025.10 subset documented in `docs/development/design-system.md`. Edit source token files and run `pnpm tokens:generate`; never edit generated CSS, JavaScript, or declarations directly. Shared primitives belong in `@dataground/ui`, remain free of product authorization and data loading, and require a Storybook contract plus focused tests before adoption by the workbench.

Keep requested and observed state distinct. Render waiting, degraded, cancelling, failed, and unknown states explicitly. Event-rich interactions include tools, processes, files, questions, approvals, usage, lifecycle, and artifacts rather than reducing every runtime to chat messages. Approval interfaces must not imply authority that policy and enforcement cannot represent.

## Verification

Use the Go, Node.js, and pnpm versions pinned by `.go-version`, `.nvmrc`, and `package.json`. Install JavaScript dependencies with `pnpm install --frozen-lockfile`. Run `pnpm verify` for the repository-wide baseline: formatting, linting, contract and generated-artifact checks, pinned development-profile checks, type checking, Go, token, UI, workbench, browser-story and accessibility tests, and production builds. GitHub CI additionally runs the live enforcement-object subset against the pinned disposable backend candidate. After an accepted OpenAPI change, run `pnpm contracts:generate`; never edit generated contract types directly. Use `pnpm dev:api`, `pnpm dev:workbench`, and `pnpm dev:design-system` for the development processes.

Behavior changes require tests at the appropriate contract, integration, security, resilience, or accessibility layer. Do not claim a check passed unless it ran; report checks that could not run and why.

Before finishing substantive work, review the complete change in five passes: factual and contract accuracy; scope and instruction precedence; security and isolation; failure recovery and operability; then maintainability, documentation, and diff regression. Retain only changes that improve the outcome. Do not put pass scores or procedural notes in repository documentation, commits, or pull request descriptions.

## Maintaining this file

Use `.agents/skills/update-agents-md/SKILL.md` when creating or substantially revising agent guidance. Keep this file concise and repository-specific. Add durable instructions only after verifying them against the current tree and toolchain, remove obsolete guidance when the repository changes, and place detailed repeatable procedures in focused documentation or skills rather than expanding this file indefinitely.

