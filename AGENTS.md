# DataGround agent guidance

DataGround is security-sensitive platform infrastructure for data workloads, notebooks, coding agents, and persistent assistants. Correctness, fail-closed behavior, stable contracts, maintainability, and operational clarity take priority over expedient changes.

## Current repository state

This repository is an initial scaffold. It does not yet contain application code, build manifests, deployment configuration, canonical architecture documents, or verified build and test commands. Do not invent repository paths, commands, supported behavior, dependency choices, or production guarantees. Introduce them only with the implementation and evidence that make them true, and update this guidance in the same change when they become durable repository facts.

The first implementation milestone is a governed agent-service vertical slice. It must establish platform-native resources, immutable revisions, invocations, typed event replay, cancellation, artifacts, usage, audit, and a deterministic reference runtime before integrating broad notebook, lakehouse, job, or compatibility surfaces.

## Architecture boundaries

Public clients use DataGround APIs and resource identities. They must never receive OpenShell gateway addresses, sandbox ports, Kubernetes credentials, root storage credentials, or native harness endpoints.

Cedar expresses authorization intent. Rosetta is the Cedar-to-OpenShell materialization boundary; do not add a fallback compiler inside DataGround. An unavailable or unrepresentable translation fails closed. OpenShell provides the sandbox execution and enforcement boundary, while DataGround owns product identity, authorization, durable state, placement, lifecycle, audit, and public contracts.

Provider credentials must never be readable by notebook or harness processes. Use OpenShell-mediated credentials or an explicitly designed credential-holding bridge. Treat raw provider-secret visibility as a configuration and release failure.

Durable lifecycle state belongs in PostgreSQL-backed, finite resource state machines. Workers are replaceable, state transitions and outbox records commit atomically, timers and retries are durable, and ambiguous external effects are observed before repetition. Do not introduce a general workflow engine or generic workflow abstraction without a new architectural decision supported by measured need.

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

Keep requested and observed state distinct. Render waiting, degraded, cancelling, failed, and unknown states explicitly. Event-rich interactions include tools, processes, files, questions, approvals, usage, lifecycle, and artifacts rather than reducing every runtime to chat messages. Approval interfaces must not imply authority that policy and enforcement cannot represent.

## Verification

No repository-wide build or test baseline exists yet. Do not claim one. For the current documentation scaffold, verify Markdown structure, repository-relative links, and the complete diff. When a toolchain is introduced, add reproducible local commands and CI in the same change, then replace this paragraph with the verified baseline.

Behavior changes require tests at the appropriate contract, integration, security, resilience, or accessibility layer. Do not claim a check passed unless it ran; report checks that could not run and why.

Before finishing substantive work, review the complete change in five passes: factual and contract accuracy; scope and instruction precedence; security and isolation; failure recovery and operability; then maintainability, documentation, and diff regression. Retain only changes that improve the outcome. Do not put pass scores or procedural notes in repository documentation, commits, or pull request descriptions.

## Maintaining this file

Use `.agents/skills/update-agents-md/SKILL.md` when creating or substantially revising agent guidance. Keep this file concise and repository-specific. Add durable instructions only after verifying them against the current tree and toolchain, remove obsolete guidance when the repository changes, and place detailed repeatable procedures in focused documentation or skills rather than expanding this file indefinitely.
