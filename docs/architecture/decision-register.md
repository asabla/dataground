# DataGround Architecture Decision Register

Status: Active
Decision process initiated against Draft 0.3; reconciled with Draft 0.4.1, 2026-07-15
Purpose: Resolve the decisions required before implementation freeze in dependency order.

## Decision principles

- Multi-tenancy is a first-class capability, not a mandatory user experience.
- A self-hosted Kubernetes deployment is the authoritative production reference.
- Local development must support both Docker-based and local-Kubernetes workflows.
- Agent services are the first end-to-end product slice.
- Decisions are marked Confirmed only when the product boundary and acceptance consequence are explicit.

## Confirmed decisions

### ADR-001: Hierarchical and optional tenancy boundaries

**Status:** Confirmed
**Resolves:** Specification decision 4, Tenancy boundary

DataGround shall model isolation as a hierarchy rather than treating every user or team as a tenant:

```text
Installation / operator boundary
└── Organization or tenant isolation domain (optional as a user-facing concept)
    └── Team
        └── Workspace
```

A single trusted organization may operate team-isolated workspaces without exposing or requiring tenant administration. Deployments that host mutually untrusted organizations may activate the stronger tenant isolation domain. Users, teams, and workspaces do not become tenants merely because authorization boundaries exist between them.

**Required consequences**

- Identity, authorization, quotas, encryption, storage prefixes, audit, and workload placement carry an explicit isolation-domain identifier.
- Team isolation and hostile-tenant isolation are separate deployment/security profiles with separate acceptance tests.
- Cross-tenant access is deny-by-default and cannot be granted by workspace administrators.
- Cross-team collaboration within one organization is policy-controlled and does not require tenant federation.
- Operator roles do not receive implicit access to tenant, team, workspace, notebook, agent-state, or data contents.
- The UI may omit tenant concepts in a single-organization installation while the underlying model remains tenant-capable.

**Validation gates**

- Demonstrate team-isolated workspaces in a single-organization deployment.
- Demonstrate namespace, identity, network, secret, storage, cache, queue, and audit separation for hostile tenants.
- Verify that resource identifiers and authorization-cache keys cannot collide across isolation domains.

### ADR-002: Self-hosted-first hybrid deployment with two local development modes

**Status:** Confirmed
**Resolves:** Specification decision 6, Infrastructure target

The authoritative production profile is self-hosted Kubernetes. DataGround shall remain deployable on conformant public-cloud Kubernetes, but cloud-provider-specific services must stay behind replaceable interfaces. Local development shall support both a Docker-based mode and a local-Kubernetes mode selected by the developer.

**Deployment profiles**

| Profile | Purpose | Baseline |
| --- | --- | --- |
| Local Docker | Fast component and API development | Docker Compose or equivalent; reduced scale; explicit capability gaps |
| Local Kubernetes | Production-like integration development | kind, k3d, or equivalent; Tilt or a comparable inner-loop orchestrator |
| Self-hosted Kubernetes | Authoritative production reference | Conformant Kubernetes, CSI storage, S3-compatible objects, OIDC, workload identity |
| Cloud Kubernetes | Supported secondary production profile | Same platform contracts; managed substitutes permitted behind adapters |

**Required consequences**

- One declarative configuration model feeds all profiles; profiles may narrow capabilities but cannot silently change security semantics.
- Production manifests and controllers are Kubernetes-native; Docker mode uses adapters or emulation only where necessary.
- Storage, identity, secrets, ingress, observability, and GPU integrations have provider-neutral contracts and conformance tests.
- The local Kubernetes inner loop shall support selective service startup, live rebuild/redeploy, logs, port forwarding, seed data, and repeatable teardown.
- Unsupported local features must fail explicitly or be replaced by documented test doubles.

**Validation gates**

- Run the same agent-service contract suite in Local Docker, Local Kubernetes, and Self-hosted Kubernetes profiles.
- Prove that an environment/revision bundle promoted from local Kubernetes can run unchanged in the production reference profile, except for declared configuration and secret bindings.
- Publish a portability matrix for storage, ingress, identity, GPU, and sandbox-driver behavior.

### ADR-003: Agent services are the first end-to-end MVP

**Status:** Confirmed
**Resolves:** Specification decision 3, Primary workloads; establishes direction for decisions 15, 26, and 27

The first product slice shall expose a governed agent runtime as a versioned service. The slice must prove runtime execution, policy, events, state rules, artifacts, native programmatic invocation, and model-compatible invocation before lakehouse and notebook parity become the critical path.

**MVP vertical slice**

- Publish an immutable agent-service revision behind a stable alias.
- Invoke it through the platform-native API.
- Stream and replay typed events.
- Enforce Cedar-derived runtime and tool capabilities.
- Persist outputs, artifacts, usage, audit, and revision provenance.
- Expose the service through the platform-native interactive and programmatic surfaces; compatibility facades follow later.
- Run it in local Docker, local Kubernetes, and the self-hosted Kubernetes reference profile.

**Scope consequences**

- Notebook support remains part of DataGround, but it no longer defines the first product proof.
- Iceberg, Spark, Trino, MLflow, and general workflow orchestration move behind the agent-service vertical slice unless directly required by its first use case.
- The MVP release scope includes the complete initial harness matrix and Hermes profile support. Implementation remains contract-first and incremental so each adapter proves the same platform semantics rather than defining a separate product boundary.

**Exit gate**

One authorized consumer can publish, invoke, observe, cancel, retry, audit, and retire a service revision without direct access to the sandbox provider, Kubernetes API, root storage credentials, or upstream harness protocol.

### ADR-004: All four runtime families are first-class initial targets

**Status:** Confirmed; concrete versions are release-manifest data
**Resolves:** Runtime-family portion of specification decision 15, Harness priority

DataGround shall support Codex, Claude Code, OpenCode, and Hermes as first-class runtime families in the initial product scope. No one upstream harness protocol becomes the DataGround product API.

The coding harnesses share the provider-neutral Agent Harness API. Hermes uses the same event, policy, tool, artifact, identity, audit, and service-publication foundations, but retains a distinct Profile API for persistent state, hibernation, schedules, memory, skills, and messaging.

**Implementation sequence**

1. Freeze the normalized lifecycle, capability, approval, question, event, artifact, usage, and error contracts.
2. Build the contract test kit and a deterministic reference adapter/fake runtime.
3. Implement Codex, Claude Code, and OpenCode adapters against the same tests.
4. Implement the Hermes profile adapter and persistence extensions without widening the common invocation contract.
5. Run cross-adapter differential tests and publish an explicit capability/degradation matrix.

Adapters may be completed sequentially, but initial-release readiness requires all four certified against the contract. A feature present in only one runtime is exposed as a negotiated capability, not silently emulated by the platform.

Exact upstream versions, model/provider configurations, authentication modes, and image digests are pinned in the release certification manifest defined by ADR-022. They are release data rather than permanent architecture choices. Mutable `latest` versions are prohibited in certification and production manifests.

### ADR-005: Three initial published service templates

**Status:** Confirmed
**Resolves:** Specification decision 26, First service surfaces/use case

The initial product shall publish three reference service templates through the platform-native service API and interactive event contract:

| Template | Primary runtime class | Required outputs and behavior |
| --- | --- | --- |
| Repository engineering agent | Codex, Claude Code, or OpenCode | Repository snapshot/ref input; streamed tool/process/change events; patch, commit-ready change set, diagnostics, and artifacts |
| Data and research agent | Coding harness or Hermes, selected per revision | Typed question/task and governed data/tool bindings; citations/provenance, datasets, reports, code, and artifacts |
| Persistent personal or team assistant | Hermes profiles hosted in an OpenShell sandbox | Durable profile-scoped memory; cooperating profiles, ephemeral delegated sub-agents, skills, schedules, messages, files, approvals, checkpoints, export, and deletion |

These are templates over shared platform resources, not hard-coded service types. Each template binds an immutable revision, input/output schemas, runtime capabilities, policy bundle, state mode, declared resource limits and metering, data/tools, and rollout configuration.

**Required consequences**

- Repository credentials and source snapshots use scoped, short-lived grants; arbitrary host checkouts are prohibited.
- Research outputs preserve source and tool provenance and distinguish retrieved evidence from model inference.
- Personal and team assistants have distinct sandbox and profile ownership, controller, observer, memory, retention, and deletion policies; each sandbox may contain several independent Hermes profiles and their ephemeral delegated sub-agents.
- A team assistant cannot inherit every team member's permissions; each action is evaluated for the initiating principal and service identity.
- The same runtime may back several templates, and a template may support several compatible runtimes, without changing the public service contract.

**Initial-release gate**

At least one conforming revision of each template must complete publish, invoke/interact, observe, cancel, audit, upgrade, and retire tests. The three templates do not need identical runtime capabilities.

### ADR-006: Platform-native API precedes OpenAI compatibility

**Status:** Confirmed
**Resolves:** Launch-scope portion of specification decision 27, OpenAI compatibility profile

The first release shall expose the platform-native Agent Service API and interactive event protocol. OpenAI-compatible Models, Responses, and Chat Completions endpoints are not launch requirements.

This preserves native semantics for typed events, tools, processes, files, approvals, questions, steering, usage, artifacts, schedules, and persistent profile behavior. The native contract must not be constrained to the least expressive model API.

**Required consequences**

- OpenAI compatibility remains a planned facade over published service revisions, not a runtime integration mechanism.
- Native resource identifiers and event envelopes shall be stable enough to support a future compatibility mapper.
- Compatibility-specific state such as `previous_response_id`, `store`, and response deletion is not added to the native API unless it has independent platform meaning.
- Future compatibility work begins with Models and Responses. Chat Completions is optional and must declare semantic loss explicitly.
- Official SDK/version certification is deferred until the compatibility milestone and does not block the native MVP.

**Exit gate**

All three initial service templates can be used without an OpenAI-compatible facade, including streaming/replay, asynchronous invocation, cancellation, approvals, artifacts, and audit.

### ADR-007: REST and SSE establish semantics before bidirectional WebSocket

**Status:** Confirmed
**Resolves:** Specification decision 16, Event transport

The initial release shall provide REST lifecycle and command operations, SSE event streaming and replay, and bidirectional WebSocket interaction. WebSocket is required for release but is implemented last so it reuses stable command and event semantics rather than creating a separate protocol model.

**Implementation sequence**

1. Define the canonical append-only event envelope, cursor, ordering, idempotency, terminal states, and retention behavior.
2. Implement REST resources and commands for publish, invoke, inspect, cancel, approve, answer, steer, and retrieve artifacts.
3. Implement SSE live streaming, reconnect, cursor-based replay, backpressure, and artifact spillover.
4. Implement WebSocket as a bidirectional transport for the same commands and events.

**Required consequences**

- REST, SSE, and WebSocket use the same resource identifiers, authorization rules, event types, sequence numbers, and error model.
- WebSocket does not expose upstream harness frames or bypass the control plane.
- Reconnection resumes from an acknowledged event cursor and detects retention gaps explicitly.
- Commands carry idempotency keys and expected interaction/turn versions where conflicts are possible.
- Large or slow payloads become governed artifacts rather than blocking the realtime channel.

**Exit gate**

Recorded interaction traces replay to equivalent client state over SSE and WebSocket, including reconnect, duplicate delivery, delayed consumer, approval, steering, cancellation, and terminal failure cases.

### ADR-008: State and placement defaults depend on service semantics

**Status:** Confirmed
**Resolves:** Specification decision 28, State defaults

DataGround shall select the state contract by service type. Placement is an internal implementation decision and cannot silently change the externally declared state behavior.

| Service behavior | State contract | Default placement |
| --- | --- | --- |
| Ordinary native API invocation | Stateless across invocations; durable outputs and audit remain | Per-invocation worker or verified clean pool |
| Interactive repository or research session | Dedicated, time-bounded interaction state | Dedicated session sandbox with explicit checkpoint/export |
| Persistent personal or team assistant | Durable governed Hermes homes per profile, plus sandbox-shared collaboration state | OpenShell sandbox hosting one or more Hermes profiles, with sandbox placement lease and profile-aware checkpoint, hibernation, restore, export, and deletion |

Clean warm pools are an internal optimization. Before reassignment, a worker must pass reset verification for processes, filesystem, environment, credentials, network state, caches, logs, runtime conversation, and provider state. A failed reset destroys the worker.

Sticky-caller and shared mutable state are opt-in revision properties, never inferred from repeated calls or routing affinity. Every published revision declares its state contract, retention, concurrency, placement eligibility, and recovery behavior.

**Exit gate**

Cross-caller and cross-tenant reset tests demonstrate no state leakage, while dedicated sessions and persistent Hermes sandboxes demonstrate declared recovery and deletion semantics.

### ADR-009: Interactions may have several simultaneous controllers

**Status:** Confirmed
**Resolves:** Specification decision 29, Interactive control

An interaction may have several concurrently authorized controllers. Controllers can submit input, steer work, interrupt, answer questions, and perform approvals when their individual policy grants permit it. Observer-only roles remain available.

Simultaneous control does not mean unordered mutation. The interaction gateway assigns every accepted command a total order in the canonical event log.

**Required consequences**

- Every command records its initiating principal, service identity, connection, client sequence, server sequence, policy decision, and resulting events.
- Authorization is evaluated per controller and per action. Controllers cannot combine their separate privileges into a more powerful composite identity.
- Conflicting commands use expected-version checks and explicit conflict responses; last-write-wins is prohibited for control, approval, policy, or state mutations.
- The UI shows controller presence, command authorship, pending actions, and the current ordered state.
- Approvals record the actual approver. A controller without the required grant cannot approve through another controller's session.
- Runtimes that cannot accept mid-turn steering expose the limitation through capability negotiation; the gateway queues, rejects, or converts steering to an interrupt-and-resume operation according to declared policy.
- Disconnecting one controller does not terminate the interaction while another authorized controller remains, unless the interaction policy says otherwise.

**Exit gate**

Concurrency tests cover simultaneous prompts, steering, interrupt, approval, answer, disconnect, reconnect, and role revocation without lost commands, privilege union, ambiguous authorship, or divergent replay.

### ADR-010: The workbench is a multi-protocol client

**Status:** Confirmed
**Resolves:** Specification decision 1, Frontend protocol

The DataGround workbench shall not impose one wire protocol on all workloads. Jupyter Server WebSocket semantics are used only for notebook kernel interaction. Agent harnesses and Hermes use the platform-native REST, SSE, and WebSocket interaction contracts defined by ADR-007.

The frontend consumes a shared product-level resource model for workspaces, identities, revisions, services, sessions, artifacts, policy decisions, and audit references while using protocol-specific client modules for workload interaction.

**Protocol boundaries**

| Workbench capability | Protocol/client contract |
| --- | --- |
| Notebook kernel execution | Jupyter Server-compatible WebSocket messages through the platform bridge |
| Agent invocation and lifecycle | Native REST resources and commands |
| Agent event stream and replay | Native SSE event envelope |
| Live steering and collaborative control | Native bidirectional WebSocket, implemented after REST/SSE |
| Artifacts and large outputs | Governed HTTP artifact API with signed or proxied transfer |
| Administrative configuration | Versioned platform REST API |

No browser client receives OpenShell, Kubernetes, raw kernel connection, upstream harness, catalog-root, or object-store-root credentials. Protocol adapters remain behind platform authorization and resource APIs.

**Exit gate**

Notebook and agent interactions can coexist in one authenticated browser session without translating agent events into Jupyter messages or leaking upstream provider protocols into shared UI state.

### ADR-011: Monorepo now, independently deployable units

**Status:** Confirmed
**Resolves:** Specification decision 2, Frontend source and deployment

The initial DataGround source layout shall use one repository for the workbench and platform services. The workbench remains an independently buildable, versioned, and deployable unit. Repository co-location is a development and coordination choice, not permission for compile-time coupling to service internals.

**Required consequences**

- API schemas, event schemas, generated clients, UI packages, and conformance fixtures have explicit ownership and versioning inside the repository.
- Frontend builds consume published contracts rather than importing backend implementation modules.
- CI can test, build, version, and release the workbench separately from control-plane and runtime services.
- Deployments may serve the workbench through the same ingress, but the control plane does not need to embed its compiled assets.
- Repository boundaries may be split later without changing product APIs, resource identifiers, authentication, or deployment topology.
- Local Docker and local Kubernetes profiles support selective startup and rebuild of the frontend independently.

**Exit gate**

The workbench can be built and deployed from the monorepo against a separately running compatible control-plane version using only declared contracts and configuration.

### ADR-012: One modular DataGround workbench

**Status:** Confirmed additional product decision
**Resolves:** Product interface structure added during decision review

DataGround shall present one workbench for agent services, notebooks, data, jobs, artifacts, and administration. The application uses modular feature areas and shared navigation rather than separate products.

**Required consequences**

- Workspace, organization/tenant, team, identity, policy, artifact, activity, and audit context is shared across feature areas.
- Agent, notebook, data, job, and administration modules may be loaded and released independently inside the application shell.
- Route-level authorization prevents hidden modules from becoming metadata-disclosure paths.
- A deployment may disable modules without forking the workbench.
- Long-running interactions survive navigation through durable server state and reconnectable event clients.
- Accessibility, keyboard navigation, responsive behavior, error handling, and observability are application-wide contracts.

**Exit gate**

One user can move between a repository-agent interaction, a data/research interaction, a Hermes profile, a notebook, and their resulting artifacts without losing workspace identity, execution context, or audit linkage.

### ADR-013: Compliance certification is a later external workstream

**Status:** Confirmed
**Resolves:** Specification decision 12, Compliance

The initial DataGround release shall implement a strong, configurable security and privacy baseline without claiming a named certification. Formal compliance assessment and certification are separate external workstreams started when the system is functionally complete for commercial sale or when a specific compliance posture becomes a differentiating sales requirement.

This defers certification, not evidence quality. DataGround must retain the controls and evidence needed to avoid an expensive compliance retrofit.

**Baseline requirements**

- Configurable data residency, retention, deletion, encryption, backup, access review, and audit policies at installation and organization/tenant scope.
- Encryption in transit and at rest, with replaceable key-management integrations and tenant-aware key references.
- Immutable version and provenance records for policies, service revisions, environments, runtime images, grants, executions, and administrative changes.
- Exportable evidence for identity lifecycle, privileged access, policy changes, vulnerability management, backup restoration, incident response, and supply-chain integrity.
- No marketing or contractual claim of GDPR readiness, ISO 27001, SOC 2, HIPAA, or another named posture until the corresponding legal and independent assessment process is complete.
- A future compliance profile may tighten configuration and operational requirements without changing public product APIs.

**Trigger for a certification ADR**

Commercial readiness, a target customer requirement, or an explicit product-positioning decision must identify the jurisdiction, framework, scope, auditor or assessor, required evidence period, data classes, and launch dependency.

### ADR-014: Configurable policy-administration separation profiles

**Status:** Confirmed
**Resolves:** Specification decision 13, Policy administration

DataGround shall offer three policy-administration profiles. Deployments begin from maintained templates and may select a stricter profile as operating experience and customer requirements develop.

| Profile | Intended use | Authoring and activation model |
| --- | --- | --- |
| Owner-managed | Local development or small trusted installation | One explicitly authorized policy administrator may author and activate changes |
| Reviewed | General production default | Policy author proposes; a different authorized reviewer activates |
| High-assurance | Hostile multi-tenant or regulated operation | Platform security owns the mandatory baseline; organization/tenant authors propose scoped overlays; independent reviewers activate; workspace administrators only bind approved templates and parameters |

**Invariants in every profile**

- Schema validation, static analysis, tests, simulation, immutable versioning, change rationale, audit, rollback, and emergency revocation remain mandatory.
- A parent isolation domain can require a minimum administration profile; a child domain cannot weaken it.
- Organization/tenant and workspace overlays may only narrow or extend within explicitly delegated actions and resource types.
- Emergency revocation uses a narrowly held role, terminates or re-evaluates affected executions, and cannot silently author broader grants.
- Template selection and parameter binding are authorization decisions, not unreviewed text substitution.
- Moving to a less separated profile is a high-risk, audited administrative change and cannot take effect through workspace self-service.

**Initial templates**

DataGround shall ship Development, Standard Production, and High-assurance Multi-tenant templates corresponding to the three profiles. The templates include example roles, Cedar actions, activation workflow, emergency controls, tests, and operating guidance.

### ADR-015: Headless actions require both authorization and representable enforcement

**Status:** Confirmed after OpenShell validation
**Resolves:** Specification decision 30, Headless actions

Headless actions use a risk-tiered product policy, but execution is permitted only when DataGround can compile the grant into the narrowest enforceable runtime and application controls. A broad label such as `reversible` or `read-only` is not itself an enforcement mechanism.

**Decision model**

1. Cedar evaluates the principal, service revision, action, target resource, data class, execution context, budget, and requested capability.
2. The compiler materializes permitted sandbox controls for the concrete execution.
3. OpenShell enforces representable filesystem, process, network, credential, and inference boundaries.
4. DataGround's capability broker or an application-side policy adapter enforces business operations that OpenShell cannot express exactly.
5. If either enforcement layer is missing, ambiguous, wider than the grant, or operating in audit-only mode, the headless action is denied.

**OpenShell-aligned enforcement rules**

- Undeclared filesystem paths are inaccessible; production profiles use Landlock `hard_requirement` where filesystem isolation is required.
- Outbound network access is deny-by-default and bound to declared destination, port, calling binary, and—where supported—protocol-specific L7 rules.
- REST grants use exact methods and paths where feasible. OpenShell's `read-write` preset permits POST, PUT, and PATCH but does not prove business reversibility; `full` also permits destructive methods and is not an acceptable proxy for a safe mutation tier.
- GraphQL mutations, MCP methods/tools, JSON-RPC methods, and inspected WebSocket operations use explicit rules rather than a generic host grant.
- Uninspected TCP protocols, opaque encrypted flows, WebSocket binary-frame semantics, or endpoints requiring overly broad wildcards cannot carry preapproved headless mutations unless another trusted broker enforces the exact operation.
- Credentials are attached through governed providers or short-lived grants and are scoped independently from network reachability.
- Application-delivered policy hints improve planning and user experience but are not trusted as the sole security boundary.

**Risk tiers**

| Tier | Headless behavior |
| --- | --- |
| Read and inspect | May be preapproved when exact data, filesystem, network, tool, and credential grants are enforceable |
| Reversible or idempotent mutation | Requires an explicit immutable service-revision grant, bounded target, idempotency protection, compensating/retry semantics, and exact enforcement |
| Irreversible, high-impact, privilege-changing, or unrepresentable mutation | Denied headlessly; requires a separate approved workflow or interactive authorization |

OpenShell Policy Advisor is a runtime network-policy proposal mechanism, not the DataGround authorization source. Its auto mode only establishes that the OpenShell prover found no flagged capability delta; it does not establish business approval. DataGround must independently authorize any policy change and must not let a sandbox widen its own effective grant.

**Validation basis**

- [OpenShell policy schema](https://docs.nvidia.com/openshell/reference/policy-schema)
- [OpenShell security best practices](https://docs.nvidia.com/openshell/security/best-practices)
- [OpenShell Policy Advisor](https://docs.nvidia.com/openshell/sandboxes/policy-advisor)
- [How OpenShell works](https://docs.nvidia.com/openshell/about/how-it-works)

**Exit gate**

Differential tests prove that every allowed headless action succeeds through both authorization and runtime/application enforcement, while widened methods, paths, tools, targets, binaries, credentials, and uninspectable alternatives fail and produce correlated audit evidence.

### ADR-016: OIDC/OAuth externally, workload identity and mTLS internally

**Status:** Confirmed
**Resolves:** Specification decision 31, Service credentials

DataGround shall use OIDC/OAuth for human users and external workload clients. Internal service-to-service and sandbox-to-platform communication shall use short-lived workload identity bound to mTLS. API keys are disabled by default and may be enabled only for explicitly declared legacy integrations that cannot use OAuth or mTLS.

**Required consequences**

- Browser clients use Authorization Code with PKCE and do not receive reusable service credentials.
- CLI and device-constrained clients use an appropriate OAuth flow rather than copied browser tokens.
- External machine clients use OAuth client credentials, workload federation, or mTLS-bound credentials according to deployment capabilities.
- Internal services, sandboxes, jobs, distributed compute, and capability brokers receive short-lived, audience-bound identities; Kubernetes ServiceAccount identity is exchanged rather than treated as a general bearer credential.
- API keys, when enabled, are hashed at rest, scoped to one organization/tenant and service audience, time-bounded where possible, rotatable with overlap, individually revocable, rate-limited, and never accepted as internal workload identity.
- Credential identifiers, issuance, use, rotation, and revocation are audited without logging secret values.
- Local development may use a development identity provider or explicitly marked local credentials, but production authentication semantics and audience checks remain testable.

**Exit gate**

Cross-tenant, wrong-audience, expired, revoked, replayed, and downgraded credentials fail across public APIs, internal services, sandbox sessions, callbacks, and realtime reconnect paths.

### ADR-017: Content deletion and mandatory audit are separate lifecycles

**Status:** Confirmed provisional baseline
**Resolves:** Specification decision 34, Compatibility retention

User and service content follows organization/tenant retention, export, legal-hold, and deletion policy. Mandatory security, administrative, billing/usage, and provenance records follow a separately configured audit-retention policy. Deleting content does not silently delete evidence that an authorized operation occurred, but retained audit data must be minimized and must not preserve the deleted payload by accident.

**Required consequences**

- Events and records classify fields as content, derived output, operational telemetry, security audit, usage, or immutable provenance.
- Content deletion removes or cryptographically erases primary payloads, artifacts, search indexes, caches, checkpoints, replicas, and derived previews according to the declared deletion SLA.
- Retained audit uses identifiers, hashes, decision metadata, timestamps, actors, actions, outcomes, and tombstones where sufficient; prompts, responses, files, secrets, and personal memory are not copied into audit by default.
- Legal hold is explicit, authorized, discoverable to administrators, scoped, and audited.
- Conversation or response references to deleted content return a stable deletion result and cannot reconstruct the content from caches or event replay.
- A future OpenAI-compatible `store=false` suppresses optional response-content persistence but does not disable the minimum mandatory audit disclosed by DataGround.
- The provisional baseline must be revisited by the external compliance/privacy workstream defined in ADR-013.

**Exit gate**

Deletion tests trace one payload through events, artifacts, Hermes sandbox state, profile references, indexes, caches, backups, exports, and audit, proving that content disappears within policy while the minimal authorized tombstone and security evidence remain.

### ADR-018: Three Cedar-governed runtime policy-escalation modes

**Status:** Confirmed
**Refines:** ADR-015 and the OpenShell Policy Advisor operating model

DataGround shall expose three policy-escalation modes. They control whether a running sandbox may request additional dynamic capability after a denial. The effective mode is calculated from Cedar-governed installation, organization/tenant, workspace, service-revision, principal, and invocation policy. A lower scope may select an equally restrictive or stricter mode but cannot exceed a parent ceiling.

| Mode | Behavior | Intended use |
| --- | --- | --- |
| Locked | Sandbox-originated proposals are disabled. Undeclared access is denied; widening requires a new approved revision or operator-controlled redeployment. | High-assurance tenants, production headless services, fixed workloads |
| Semi-interactive | Default. A denied dynamic network capability may create a narrow proposal for an authorized human or external approval workflow. Cedar and the OpenShell prover must both accept it before hot reload. | Interactive coding, research, administration, controlled asynchronous work |
| Auto | Proposals may be approved without a human only inside an explicit global Cedar ceiling and only when OpenShell reports an empty prover delta. | Curated low-risk automation and explicitly enabled environments |

**Mode semantics**

- The sandbox, harness, model, tool, and service owner cannot select or weaken the mode at runtime.
- OpenShell Policy Advisor proposals are treated as untrusted requests. DataGround re-normalizes the requested binary, destination, protocol, method/path/tool, credential reach, and duration before Cedar evaluation.
- OpenShell prover success is necessary in Auto mode but is not sufficient; Cedar must permit the exact capability and `AutoApproveCapability` for the effective scope.
- Auto mode cannot create a business-operation grant absent from the immutable service revision. It may only materialize a runtime rule already within the preauthorized global and revision ceilings.
- Static filesystem, Landlock, process, identity, mount, and compute changes never hot-reload through this workflow. They require a new execution or revision and the normal approval path.
- Narrowing and emergency revocation remain available in every mode and take precedence over pending or previously approved proposals.
- Approved dynamic grants have explicit provenance, scope, expiry, and revocation behavior; they do not become undeclared global defaults.

**Interaction behavior**

- Interactive sessions expose a typed `capability.requested` event with requested rule, rationale, policy evaluation, prover findings, approver requirements, and deadline.
- Synchronous headless invocation never waits for approval. It fails with a structured unrepresentable-or-not-preapproved result.
- Asynchronous invocation may enter a bounded `waiting_for_approval` state only when the revision permits it, an authorized approval route exists, and a deadline/failure behavior is declared.
- Expired, rejected, revoked, disconnected, or unrouteable requests fail closed and produce a terminal or resumable event according to the service contract.

**Exit gate**

The same denied operation is tested under all three modes. Locked denies without proposal, Semi-interactive requires the correct approver and bounded wait, and Auto succeeds only inside both the Cedar ceiling and empty-prover-delta condition. Parent policy, revocation, expiry, and static-control attempts must override or fail in every case.

### ADR-019: Rosetta is the external Cedar-to-OpenShell compiler

**Status:** Confirmed dependency; v1 contract candidate available, release certification pending
**Resolves:** Status portion of specification decision 14, Translator status

The Cedar-to-OpenShell compiler is the separately built [Rosetta](https://github.com/asabla/rosetta) service. Rosetta main at commit [`320158f1e4a4eea378d82c1527f4a7af5fb9855b`](https://github.com/asabla/rosetta/commit/320158f1e4a4eea378d82c1527f4a7af5fb9855b) declares compiler version `1.0.0`, catalog `rosetta/v1`, `POST /v1/compile`, and OpenShell target contract `rosetta/openshell-policy-v1`. No corresponding `v1.0.0` release tag exists as of 2026-07-21, so this is a contract candidate rather than a production release. DataGround owns the consumer contract, provenance requirements, conformance suite, availability behavior, and safe integration; Rosetta owns translation implementation.

Rosetta is an external component boundary, not necessarily an external SaaS dependency. The reference deployment must support running a pinned Rosetta build under the operator's control.

**Required service contract**

Rosetta receives only immutable references or normalized data required to materialize one execution:

- Cedar schema and policy-bundle versions;
- normalized entity snapshot and authorization decisions;
- principal, action, resource, and relevant execution context;
- requested concrete capabilities after platform ceilings;
- target OpenShell policy-schema and runtime version;
- environment and workload facts needed for exact path, binary, endpoint, and compute mappings.

It returns:

- one immutable OpenShell enforcement document or an explicit failure;
- a per-capability mapping result: exact, narrower, denied, unsupported, or ambiguous;
- source and target hashes, compiler build identity, schema versions, diagnostics, and reproducibility metadata;
- no resolved secret values.

**Invariants**

- Unsupported, ambiguous, wider, malformed, unavailable, timed-out, or version-incompatible translation fails closed.
- DataGround never falls back to a handwritten permissive policy when Rosetta is unavailable.
- Requests and responses use workload identity and mTLS and are bound to one execution/revision context.
- Compilation is deterministic for identical normalized inputs and pinned versions.
- Rosetta output is validated independently against the pinned OpenShell schema before use.
- A deterministic fake may support local development and contract tests, but production execution requiring compilation uses a certified Rosetta build.
- Rosetta upgrades require golden tests, property/fuzz tests, differential sandbox tests, canarying, rollback, and stored provenance.

**Implementation-freeze blocker**

The candidate wire and target contracts are sufficient for an unwired, fail-closed client. Production integration remains blocked until Rosetta publishes a signed and tagged build, stable machine-readable error taxonomy, authenticated service transport profile, compatibility policy, and signed conformance fixtures. DataGround must certify that release with golden and differential OpenShell tests before admitting generated enforcement material.

### ADR-020: Harness credentials are exclusively mediated by OpenShell

**Status:** Confirmed
**Resolves:** Specification decision 17, Claude authentication; applies to every harness provider

DataGround shall support direct Anthropic and supported cloud-provider backends, but OpenShell exclusively manages provider credentials and inference routing. A harness process must never receive a real API key, refresh token, cloud secret, or reusable provider credential inside the sandbox. Observing one is a configuration failure and release blocker.

OpenShell currently supports the required pattern: the process sees an opaque placeholder and the policy proxy resolves it only in an authorized outbound request. OpenShell also exposes managed inference routing that keeps provider credentials outside the sandbox.

**Required consequences**

- Harness-visible environment variables contain only OpenShell placeholders, non-secret configuration, or short-lived sandbox identity—not raw provider secrets.
- Provider endpoints, calling binaries, request methods/paths, credential placement, inference models, and routing are generated from an approved provider profile and enforcement bundle.
- Direct Anthropic, Bedrock, Vertex/Agent Platform, Azure/Foundry, and future backends use OpenShell-supported proxy, provider, token-grant, or operator-controlled bridge patterns.
- If a provider protocol requires a raw secret for client-side signing or places credentials where the OpenShell proxy cannot safely rewrite them, DataGround uses a credential-holding bridge or marks that provider mode unsupported. It does not mount the secret into the harness.
- The harness cannot choose an arbitrary base URL that bypasses the approved provider route.
- Provider attachment, refresh, rotation, detach, expiry, model routing, and denial are audited and correlated to the service revision and execution.
- Startup and conformance tests inspect the effective process environment and outbound behavior. A real-looking credential, unresolved placeholder, bypass route, or direct provider reachability fails the execution.

**Validation basis**

- [OpenShell provider credential injection](https://docs.nvidia.com/openshell/sandboxes/manage-providers#how-credential-injection-works)
- [OpenShell Providers v2](https://docs.nvidia.com/openshell/sandboxes/providers-v2)
- [OpenShell runtime boundaries and inference routing](https://docs.nvidia.com/openshell/about/how-it-works)

### ADR-021: Native southbound adapters; ACP as a later northbound facade

**Status:** Confirmed after upstream research
**Addresses:** Specification decision 18, Codex modes, and the cross-harness maintainability strategy

DataGround should not adopt ACP as the mandatory runtime-adapter substrate today. It should integrate each runtime through its vendor-supported rich control surface behind the normalized Agent Harness API. ACP should be added later as an optional client-facing compatibility facade so ACP editors can connect to a published DataGround service.

```text
ACP editor/client -- optional ACP facade --> DataGround Agent Harness API
                                             |-- Codex app-server adapter
                                             |-- Claude Agent SDK adapter
                                             |-- OpenCode server/SDK adapter
                                             `-- Hermes Profile adapter
```

**Research findings**

| Surface | Current upstream shape | DataGround consequence |
| --- | --- | --- |
| Codex app-server | Official rich-client interface with threads, turns, approvals, steering, interrupts, items, and streamed events over JSON-RPC; stdio is the default transport | Use the stable app-server schema over stdio for the single Codex product adapter |
| Codex SDK / `codex exec` | Official automation and CI surfaces; the Python SDK itself controls a pinned app-server runtime | Do not build separate product semantics for them; retain `codex exec` as a smoke-test/fallback tool, and use an SDK only as an implementation wrapper if it preserves the required app-server events |
| Claude Agent SDK | Official programmable surface with streaming, sessions, permissions, hooks, tools, usage, and `ClaudeSDKClient` interrupts | Use `ClaudeSDKClient` for both interactive and one-shot work through one adapter |
| ACP Codex adapter | `agentclientprotocol/codex-acp` starts Codex app-server and translates events to ACP | ACP does not eliminate the Codex integration; it introduces another versioned mapping layer |
| ACP Claude adapter | `agentclientprotocol/claude-agent-acp` wraps the official Claude Agent SDK | ACP does not eliminate the Claude integration and may omit or reinterpret SDK features |
| ACP protocol | Strong editor/agent session, tool-call, terminal, permission, file, and prompt primitives | Useful compatibility surface, but not a complete match for DataGround's general services, persistent profiles, multi-controller sessions, and full event model |

**Why ACP is not the southbound canonical layer yet**

- ACP defines communication between editors and coding agents; DataGround also publishes data/research services and persistent Hermes profiles.
- Current ACP v1 has cancellation but no standard mid-turn steering/queue primitive equivalent to Codex `turn/steer`; that capability is still under discussion for v2.
- Multi-client attachment/control and several lifecycle extensions are evolving, while DataGround has already confirmed simultaneous controllers and ordered replay.
- The available Codex and Claude ACP implementations are adapters over the native upstream surfaces, so DataGround would still inherit both native protocols plus two additional adapter release trains.
- Adapter gaps can change security or behavior. Examples in upstream issue trackers include lost question events, permission-model differences, session recovery problems, and event/output regressions.

**Codex adapter decision**

- Launch one `codex app-server` process inside the sandbox and communicate over JSONL stdio.
- Use the stable API subset; experimental methods require an explicit, separately tested capability flag.
- Generate schemas from every pinned Codex version and make schema/behavior diffs an upgrade gate.
- Map thread, turn, item, approval, question, steering, interrupt, usage, and terminal/file-change events to the normalized DataGround contract.
- Run one-shot/batch work as a bounded thread/turn through the same adapter. Do not maintain a separate `codex exec` production integration.
- Do not expose app-server WebSocket outside the sandbox; DataGround owns the public REST/SSE/WebSocket surfaces.

**Claude adapter decision**

- Use the official Agent SDK with `ClaudeSDKClient`, which supports continuous sessions and interrupts; do not base the interactive product path on one-shot `query()` alone.
- Map SDK messages, `canUseTool`, questions, hooks, usage, checkpoints, and interrupts into the normalized contract.
- Treat Agent SDK permission rules as defense in depth below Cedar and OpenShell, not the source of platform authorization.
- Keep provider authentication under ADR-020; the SDK/harness receives placeholders and approved routes only.

**ACP roadmap**

- Do not include ACP in the native-first MVP critical path established by ADR-006.
- Implement one northbound DataGround-to-ACP facade after the native contract is stable.
- Declare unsupported or lossy features through ACP capability negotiation; never fabricate steering, multi-controller, persistence, or approval semantics.
- Use the maintained Codex and Claude ACP adapters as interoperability oracles in conformance tests, not required production dependencies.
- Re-evaluate southbound ACP when a runtime ships vendor-native support and passes the DataGround capability and security contract without a bridge.

**Validation basis**

- [Official Codex app-server documentation](https://developers.openai.com/codex/app-server)
- [Official Codex SDK documentation](https://developers.openai.com/codex/codex-sdk)
- [Official Claude Agent SDK overview](https://code.claude.com/docs/en/agent-sdk/overview)
- [Official Claude Agent SDK reference](https://code.claude.com/docs/en/agent-sdk/python)
- [ACP protocol overview](https://agentclientprotocol.com/protocol/v1/overview)
- [ACP tool calls and permission model](https://agentclientprotocol.com/protocol/v1/tool-calls)
- [ACP Codex adapter](https://github.com/agentclientprotocol/codex-acp)
- [ACP Claude Agent SDK adapter](https://github.com/agentclientprotocol/claude-agent-acp)

### ADR-022: One pinned harness matrix per release with managed migration

**Status:** Confirmed
**Resolves:** Remaining portion of specification decision 15, Harness priority and support targets

Every DataGround release shall publish one exact certified runtime matrix covering OpenShell, Codex, Claude Code/Agent SDK, OpenCode, Hermes, Rosetta API/schema, adapter builds, runtime images, model/provider profiles, and protocol schemas. The immediately previous certified matrix remains supported for one DataGround release cycle to provide rollback and migration overlap.

Broad semver ranges and mutable tags are not support contracts. Operators may test other versions, but a service revision using them is marked uncertified and cannot satisfy production-readiness gates.

**Release manifest**

The signed manifest records:

- exact package versions, container/image digests, generated schema hashes, and adapter builds;
- supported operating systems, architectures, compute drivers, and OpenShell policy-schema versions;
- certified provider profiles and model families;
- required, optional, degraded, experimental, and prohibited capabilities per runtime;
- compatible state/checkpoint schema versions and migration functions;
- known limitations, security advisories, rollout constraints, and rollback target;
- contract, differential, security, performance, and recovery evidence.

**Migration model**

DataGround migrates by creating a new immutable service revision and new sandbox placement. It never upgrades a harness binary, adapter, policy schema, or runtime image in place inside a running sandbox.

| Workload state | Default migration behavior |
| --- | --- |
| Stateless invocation worker | Automatic rolling replacement; new work uses the new matrix after health and canary gates |
| Clean warm pool | Build a new pool, verify reset and conformance, shift traffic, then drain and destroy the old pool |
| Active ordinary run | Remains pinned until completion or cancellation; retry uses the revision's declared matrix unless explicitly migrated |
| Interactive coding/research session | Offer restart/migrate action at a safe boundary; export declared session/workspace state, start a new sandbox, validate restore, then reconnect |
| Persistent Hermes sandbox | Acquire migration lease, quiesce every resident profile gateway and shared Kanban dispatcher, export profile homes and shared state, create immutable checkpoint, migrate a copy, validate each profile, switch the sandbox placement pointer, retain rollback checkpoint, then retire old placement |

**Automatic and operator-triggered control**

- Stateless services and clean pools default to automatic staged rollout after certification.
- Stateful sessions and Hermes sandboxes default to an administrator-triggered or scheduled migration with a preview of compatibility, downtime, active work, storage changes, and rollback.
- The workbench provides `Migrate now`, scheduled maintenance, canary percentage, pause, resume, and rollback controls according to authorization.
- Organization/tenant policy may require automatic migration for critical security releases or require manual approval for stateful migrations.
- A migration deadline may block creation of new sessions on an expiring matrix while allowing bounded drain of existing work.
- No automated migration discards unexported state, interrupts an active turn without declared policy, or deletes the rollback checkpoint before verification and retention gates pass.

**Exit gate**

The conformance suite migrates each workload-state class from the previous matrix to the new matrix, injects failures at checkpoint, restore, health, traffic-switch, and cleanup stages, and proves idempotent resume or rollback without cross-tenant leakage or state loss.

### ADR-023: OpenCode providers are certified through OpenShell profiles

**Status:** Confirmed
**Resolves:** Specification decision 19, OpenCode support window

DataGround shall support the OpenCode version pinned in each release certification matrix and the immediately previous matrix during the one-release migration window. Provider configurations are supported only through OpenShell-governed provider profiles that pass the DataGround runtime, credential, policy, model-routing, and event conformance suites.

Administrators may add custom OpenShell provider profiles. A custom profile is organization/tenant-local and uncertified until it passes the same lint, credential-placeholder, endpoint/binary, model, denial, rotation, and regression tests. Enabling every provider known to OpenCode is not a DataGround compatibility promise.

**Required consequences**

- OpenCode never receives raw provider keys; ADR-020 applies without exception.
- The service revision binds the OpenCode version, adapter version, provider-profile revision, model configuration, and effective capability declaration.
- Provider discovery inside OpenCode cannot bypass the approved OpenShell profile or add arbitrary endpoints.
- Provider-specific features are capability-negotiated and cannot silently change the normalized event, approval, usage, or artifact contract.
- Upgrade canaries exercise questions, permissions, cancellation, session recovery, model selection, tool calls, patches, terminal output, and usage reporting before certification.

### ADR-024: Capability drift is explicit and publication-safe

**Status:** Confirmed additional runtime decision
**Resolves:** Runtime capability-drift handling added during decision review

Every environment, service revision, and adapter declares required and optional runtime capabilities against versioned DataGround capability identifiers.

- Missing or behaviorally incompatible required capabilities block environment certification, service-revision publication, or rollout.
- Missing optional capabilities produce an explicit degraded capability declaration visible to clients, operators, policy evaluation, and audit.
- The platform does not emulate security-sensitive, approval, state, steering, usage, or artifact semantics unless the emulation is itself specified, enforced, and certified.
- Capability changes between runtime matrices generate a machine-readable diff and require affected service revisions to be revalidated before traffic migration.
- An already published revision remains bound to its certified matrix; a later upstream regression cannot silently redefine it.
- Clients receive negotiated capabilities for the authorized service revision, not a union of everything any installed runtime might support.

**Exit gate**

Contract tests remove, alter, and add capabilities in simulated adapters and verify correct publication blocking, explicit degradation, client negotiation, policy behavior, migration gating, and audit output.

### ADR-025: Hermes profiles own memory; OpenShell sandboxes own isolation and placement

**Status:** Confirmed after validation against Hermes documentation
**Resolves:** Specification decision 20, Hermes state; specification decision 23, Hermes memory

DataGround may create many OpenShell sandboxes containing Hermes. A sandbox may host one or more Hermes profiles. The two resources have different meanings:

- The OpenShell sandbox is the enforced filesystem, process, network, credential, inference, placement, migration, and teardown boundary.
- A Hermes profile is an independent agent and native state boundary. Its `HERMES_HOME` contains its configuration, identity, memory, sessions, skills, cron jobs, logs, state database, and Hermes gateway state.
- A delegated Hermes sub-agent is a short-lived child `AIAgent` with fresh conversation context, restricted tools, and its own terminal session. It is not a durable profile and receives only the goal and context supplied by its parent.
- Durable collaboration among independent profiles uses Hermes's shared Kanban board or an explicit DataGround service/capability, not shared profile memory.

Hermes profiles do not provide a security sandbox. Profiles co-located inside one OpenShell sandbox therefore have soft process and filesystem separation and may be managed through one machine-level surface. Hostile organization/tenant or team boundaries require separate OpenShell sandboxes or a stronger placement profile under ADR-001.

**DataGround resource model**

| Resource | Native boundary and lifecycle |
| --- | --- |
| `HermesSandbox` | OpenShell sandbox placement, effective enforcement bundle, resident profile set, Hermes gateway topology, shared Kanban state, checkpoint bundle, and migration lifecycle |
| `HermesProfile` | One `HERMES_HOME`, agent identity, configuration, memory provider/state, sessions, skills, schedules, channel identity, and profile export/import lineage |
| `HermesDelegation` | Ephemeral parent-child task with explicit goal/context, restricted toolset, terminal session, progress, final summary, cancellation, and metering |
| `HermesKanban` | Durable task/handoff collaboration state shared by authorized profiles in the sandbox deployment |

**Persistence contract**

- DataGround preserves the Hermes-native profile boundary rather than merging several profile homes into one synthetic memory store.
- Each profile is exported, imported, retained, and deleted independently. Credentials remain external bindings and are not embedded in profile distributions or portable state.
- A sandbox checkpoint manifest references the exact exported version of every resident profile plus shared Kanban state, gateway topology, runtime matrix, effective policies, and restore order.
- Hibernating or migrating a sandbox quiesces profile gateways and dispatchers, creates consistent profile-aware exports, restores them into a new placement, validates each profile and shared board, and only then switches ingress.
- Hermes project checkpoints are not a substitute for profile backup: project rollback is session/working-tree protection, whereas DataGround recovery must preserve `HERMES_HOME` and shared operational state.
- Local Docker and local Kubernetes modes use the same manifest and export/import contract through an S3-compatible development service or equivalent filesystem-backed adapter.

**Gateway topology inside the sandbox**

- DataGround supports Hermes's default one-gateway-process-per-profile topology when independent restart and process failure domains matter.
- DataGround also supports Hermes's multiplexing gateway for many lower-traffic profiles when a single supervised inbound process is operationally preferable.
- This Hermes messaging gateway is distinct from the OpenShell gateway that provisions and controls the sandbox. APIs, resource names, telemetry, and user interfaces must not call both simply `gateway` without qualification.
- A template may create one profile or a cooperating set of profiles, Kanban roles, gateway topology, and delegation defaults inside one sandbox.

**Exit gate**

Recovery tests destroy all local placement state, reconstruct every profile and the shared Kanban board from exported durable state, and verify independent memories, sessions, skills, schedules, gateway identities, collaboration, permissions, and audit linkage. Tests also prove that delegated children begin with fresh context and that a Hermes profile alone does not satisfy a security-isolation claim.

**Validation basis**

- [Hermes profiles](https://hermes-agent.nousresearch.com/docs/user-guide/profiles)
- [Hermes memory providers and profile isolation](https://hermes-agent.nousresearch.com/docs/user-guide/features/memory-providers)
- [Hermes sub-agent delegation](https://hermes-agent.nousresearch.com/docs/user-guide/features/delegation)
- [Hermes Kanban multi-profile collaboration](https://hermes-agent.nousresearch.com/docs/user-guide/features/kanban)
- [Hermes multi-profile gateways](https://hermes-agent.nousresearch.com/docs/user-guide/multi-profile-gateways)
- [Hermes profile distributions](https://hermes-agent.nousresearch.com/docs/user-guide/profile-distributions)

### ADR-026: All channel classes are initial Hermes scope

**Status:** Confirmed
**Resolves:** Specification decision 21, Hermes channels

The initial Hermes product scope shall include the DataGround workbench and native API, webhook and callback surfaces, and the external messaging channels supported by the pinned Hermes runtime matrix. Channel breadth is a launch requirement rather than a post-launch expansion phase.

DataGround shall achieve this through one versioned channel-adapter contract rather than channel-specific behavior in the profile service. Native Hermes connectors may be reused behind that contract when they meet DataGround identity, policy, event, credential, reliability, and audit requirements.

**Channel contract**

- Normalize inbound messages, replies, edits, reactions, attachments, threads, delivery receipts, retries, rate limits, identities, channel membership, and conversation references without discarding provider-specific extensions.
- Map external identities and spaces to DataGround principals, organizations/tenants, teams, workspaces, profiles, and policy context before a message reaches Hermes.
- Keep provider credentials in governed connectors and OpenShell-mediated providers; neither Hermes nor a channel adapter receives unrestricted long-lived keys inside the sandbox.
- Require idempotency, deduplication, ordering metadata, bounded retry, dead-letter handling, replay protection, and observable delivery status.
- Treat unsupported operations as negotiated capability differences rather than silently approximating another channel's semantics.
- Version channel packs independently, certify them against the pinned runtime matrix, and allow an operator to disable or omit any pack without forking the profile service.
- Preserve the native workbench and API as control, recovery, approval, and diagnostic surfaces even when a profile primarily operates through an external channel.

**Support tiers**

Every distributed channel pack declares whether it is DataGround-certified, upstream-certified, or community/uncertified. Breadth does not imply that every connector has identical guarantees; publication and administration surfaces must expose the effective channel capabilities and support tier.

**Exit gate**

The conformance suite runs each initial channel pack through identity mapping, authorization, message and attachment delivery, threads, duplicate and out-of-order events, reconnect, credential rotation, rate limiting, approval, deletion, and audit scenarios. A channel cannot be enabled as certified when a required semantic is missing.

### ADR-027: Broad capability catalog through stable capability packs

**Status:** Confirmed
**Resolves:** Specification decision 22, Hermes capability broker

Hermes shall launch with a broad capability catalog rather than an MCP-only or narrowly curated baseline. Initial scope includes workspace and artifact operations, Git and development tools, data and query access, governed HTTP and APIs, remote MCP, browser automation, messaging, documents and media, schedules and notifications, and external-device or home-automation integrations where a certifiable provider exists.

Breadth shall be implemented outside the stable broker core through versioned capability packs. Implementation duration is not a scope-selection metric; maintainability, robustness, security, and long-term compatibility are release criteria.

**Capability architecture**

- The core broker owns discovery, typed invocation, authorization, approval, credential/provider references, usage metering, declared safety limits, cancellation, progress, artifacts, audit, and capability negotiation.
- Each capability pack owns provider-specific schemas, protocol adaptation, normalization, tests, documentation, migrations, and declared limitations.
- MCP is one provider protocol, not the universal internal model and not a requirement for providers with stronger maintained native interfaces.
- Capability identifiers and resource types are stable DataGround contracts; provider and protocol details remain behind adapters.
- Packs declare required OpenShell controls, Cedar actions, credential classes, network destinations, data classifications, side-effect and reversibility properties, approval requirements, and support tier.
- Operators choose which packs are installed and organizations/tenants choose which installed capabilities may be granted. A broad catalog never means broad default authority.
- First-party, verified third-party, upstream, and community/uncertified packs are distinguishable in discovery, policy, administration, and audit.
- A pack is independently releasable and can be disabled, quarantined, upgraded, rolled back, or replaced without changing Hermes sandbox-state or channel contracts.

**Release discipline**

A capability is advertised only when its pack passes schema, policy, credential non-disclosure, denial, retry/idempotency, cancellation, resource-limit, metering, observability, compatibility, and recovery tests appropriate to its side effects. Optional provider breadth may degrade explicitly under ADR-024; missing capabilities required by a published template block publication or migration.

**Exit gate**

The first release demonstrates representative certified packs from every initial capability class and proves that adding, upgrading, disabling, or replacing a pack requires no change to the broker core or Hermes sandbox implementation.

### ADR-028: Hermes skills begin from templates and evolve as versioned lineage

**Status:** Confirmed
**Resolves:** Specification decision 24, Hermes skills

DataGround shall ship maintained Hermes templates containing prepared profile sets, Kanban roles, gateway topology, and skill sets. Users may create, derive, publish, share, and maintain their own templates. A template can describe one profile or a complete cooperating multi-profile deployment inside one OpenShell sandbox.

Hermes differs from ordinary harness runtimes because it may improve skills over time in the background. Skill evolution is therefore a first-class, durable product workflow rather than an untracked mutation of files inside a running sandbox.

**Template and skill model**

- An immutable DataGround template revision declares resident Hermes profiles, profile distributions, prepared skills, Kanban roles/routing, Hermes gateway topology, channels, capability requirements, schedules, memory-provider configuration, policy requirements, and supported runtime matrix.
- Hermes profile distributions are the native unit for shipping a configured agent. Distribution updates preserve local memories, sessions, authentication, and user data; DataGround does not replace that behavior with a proprietary skill copier.
- Creating a sandbox from a template establishes template and distribution provenance while giving each profile its own skill-evolution lineage. Later template updates do not silently overwrite evolved profile skills.
- Users can create templates from scratch, compose their own profile distributions, derive them from maintained or shared templates, and choose whether later upstream changes are reviewed, merged, or ignored.
- A skill revision records profile, source template/distribution, human or Hermes actor, rationale, examples or observations, capability delta, tests, evaluation results, policy impact, and rollback parent.
- Hermes may use its native `skill_manage` and background self-improvement review to create or change a profile's procedural memory. Activation follows the profile/sandbox locked, semi-interactive, or automatic policy mode from ADR-018 and maps to Hermes's skill-write approval control where applicable.
- No skill improvement may silently add capabilities, credentials, network reach, data scope, or delegation authority; such changes require explicit policy re-evaluation even in automatic mode.
- Native Hermes skill formats may be stored and executed, but DataGround owns the surrounding identity, provenance, version, evaluation, activation, and rollback contract.

**Lifecycle**

Skill changes progress through draft, evaluated, eligible, active, superseded, rejected, or quarantined states. Operators and authorized users can inspect diffs, evaluation evidence, activation history, current usage, and rollback targets. A sandbox export includes its template provenance and complete active skill lineage.

**Exit gate**

A template creates a multi-profile Hermes sandbox from profile distributions; one profile proposes and evaluates a background skill improvement; policy either requests approval or activates it; the platform can attribute subsequent behavior to that exact profile and skill revision and roll back without losing that profile's memory or sessions.

**Validation basis**

- [Hermes skills and agent-managed skill writes](https://hermes-agent.nousresearch.com/docs/user-guide/features/skills)
- [Hermes profile distributions](https://hermes-agent.nousresearch.com/docs/user-guide/profile-distributions)
- [Hermes profile commands](https://hermes-agent.nousresearch.com/docs/reference/profile-commands)

### ADR-029: Measure delegation economics before building delegation budgets

**Status:** Confirmed launch-scope deferral
**Resolves:** Specification decision 25, Delegation budgets

The initial release shall not build a configurable product for cost-, token-, time-, or depth-driven Hermes delegation budgets. The interaction between agent topology, infrastructure use, model cost, background activity, channel traffic, latency, and useful outcomes is not yet understood well enough to encode a durable policy model.

DataGround shall collect the evidence needed for a later decision. This is an intentional measurement phase, not permission for unbounded execution.

**Initial telemetry contract**

- Correlate organization/tenant, team, workspace, Hermes sandbox, profile/agent, parent delegation, interaction, run, model/provider, capability, channel, and runtime-matrix identifiers.
- Measure model input/output and cached tokens, provider-reported cost where available, wall/active/idle time, queue time, retries, concurrency, delegation depth and fan-out, tool calls, channel deliveries, artifacts, and completion outcome.
- Measure attributable CPU, memory, GPU, ephemeral and durable storage, network transfer, sandbox startup/restore time, and background skill or memory activity where the infrastructure exposes reliable data.
- Record confidence and attribution gaps rather than presenting estimated cost as exact.
- Keep metering records tenant-scoped and content-minimized; prompts, messages, memory, and tool payloads are not copied into usage telemetry by default.

**Controls that still exist initially**

- Infrastructure safety ceilings, provider quotas, concurrency limits, maximum execution duration, cancellation, administrative kill controls, and denial-of-service protections remain mandatory.
- These controls are operational safeguards, not the final delegation-budget product and not a claim that DataGround can yet optimize cost versus outcome.
- The workbench exposes raw and aggregated usage evidence without prescriptive budget recommendations.

**Decision trigger**

A later ADR may define hierarchical delegation budgets only after representative coding, research, assistant, channel, and background-improvement workloads provide stable attribution data. It must distinguish infrastructure capacity protection, commercial quota, customer cost control, and agent authority rather than collapsing them into one number.

**Exit gate**

Before production launch, representative multi-agent Hermes sandboxes must emit complete, privacy-reviewed usage lineages and remain bounded by tested operational safeguards. Configurable delegation-budget enforcement is explicitly not an initial-release gate.

### ADR-030: Capacity is certified through deployment profiles

**Status:** Confirmed
**Resolves:** Specification decision 5, Initial scale

DataGround shall publish certified Developer, Team, and Production deployment profiles rather than one universal capacity number. Each DataGround release records the tested operating envelope for each profile in its signed certification manifest. Architecture contracts remain horizontally scalable beyond the certified envelope, but untested scale is not represented as certified capacity.

| Profile | Purpose | Required certification shape |
| --- | --- | --- |
| Developer | One developer using local Docker/Podman or local Kubernetes | Minimal dependencies, one local OpenShell gateway, representative harnesses, restart/rebuild workflow, and bounded resource defaults |
| Team | Shared self-hosted installation for one or several teams | Concurrent interactive and batch work, queues, quotas, durable state, failure recovery, upgrade/migration, and one or more OpenShell gateways |
| Production | Multi-team or multi-tenant self-hosted reference | High-availability control plane, multiple failure domains where configured, gateway placement, workload isolation profiles, backup/restore, observability, security evidence, and sustained-load certification |

**Certified dimensions**

The release manifest states tested concurrent sandboxes, resident Hermes profiles, interactions, queued work, event rate and retention, artifact/object throughput, database load, callback delivery, gateway count and failure behavior, recovery time, and infrastructure shape. The values are release evidence and may evolve without changing the architecture decision.

Each profile is a maintained configuration template, not a separate edition. Operators can customize it, but the workbench must show whether the resulting installation is inside, outside, or not comparable to a certified envelope.

**Exit gate**

Automated load, soak, failure, recovery, and upgrade suites publish reproducible evidence for every release/profile pair. A limit violation produces admission, queueing, or explicit degradation rather than uncontrolled overload or cross-tenant impact.

### ADR-031: Durable callbacks are dispatched by the control plane

**Status:** Confirmed after OpenShell validation
**Resolves:** Specification decision 32, Callbacks

DataGround shall provide durable signed webhooks with versioned subscriptions, idempotent event identifiers, retries, exponential backoff, dead-lettering, replay, delivery inspection, and OAuth or mTLS authentication where configured.

The default dispatcher is a DataGround control-plane workload consuming the durable platform event log. Callback delivery is not delegated to a notebook, harness, Hermes profile, or other sandbox process. This preserves delivery across sandbox teardown, avoids granting agent-controlled code arbitrary egress, and keeps callback credentials outside the sandbox.

**Delivery contract**

- A subscription binds an organization/tenant, workspace or service scope, event types, destination reference, payload schema version, authentication binding, filtering rules, retention, and enablement state.
- Each attempt includes the stable event and subscription identifiers, creation timestamp, attempt number, payload digest, and verifiable signature or authenticated transport identity.
- Delivery is at-least-once. Consumers deduplicate by event identifier; DataGround does not claim exactly-once execution at a remote system.
- Ordering guarantees are declared per subscription subject and delivery partition. Retries for one destination cannot block unrelated destinations.
- Operators can pause, inspect, test, rotate authentication, replay an authorized range, drain dead letters, and revoke a subscription without exposing credential values.
- Payloads contain references to governed artifacts when content is large or access-controlled; webhook signing does not convert a callback into authorization to retrieve unrelated data.

**OpenShell compatibility**

OpenShell supports deny-by-default sandbox egress and inspected REST rules constrained by destination host, port, calling binary, HTTP method, and path. Therefore a deliberately sandbox-originated callback can be represented as an explicit outbound `POST` rule when required.

That path is exceptional. A sandbox normally emits a platform event and the control-plane dispatcher performs delivery. If a published capability requires direct sandbox delivery, Rosetta and the OpenShell policy must bind the exact destination, method/path, binary, credential provider, and data policy; an uninspectable or overly broad mapping fails closed under ADR-015.

**Exit gate**

Tests cover duplicate, delayed, out-of-order, failed, rate-limited, revoked, rotated, replayed, oversized, cross-tenant, and malicious destinations. Separate tests prove that a sandbox without an explicit OpenShell rule cannot send the same outbound request.

**Validation basis**

- [OpenShell policy schema](https://docs.nvidia.com/openshell/reference/policy-schema)
- [OpenShell network-policy tutorial](https://docs.nvidia.com/openshell/get-started/tutorials/first-network-policy)
- [OpenShell security best practices](https://docs.nvidia.com/openshell/security/best-practices)

### ADR-032: DataGround selects an OpenShell gateway for each sandbox placement

**Status:** Confirmed
**Resolves:** Specification decision 35, Capacity and placement

An installation may register one or many OpenShell gateways. DataGround's placement responsibility is to select the eligible gateway for a new sandbox; the selected OpenShell gateway then provisions and controls the sandbox through its configured Docker, Podman, Kubernetes, or MicroVM compute driver.

DataGround does not replace the OpenShell gateway or bypass it by scheduling agent pods directly. The gateway remains the OpenShell control plane for sandbox lifecycle, providers, policy delivery, inference configuration, tunnels, and supervisor communication.

**Terminology**

- `OpenShellGateway` means the external OpenShell control plane and compute-driver boundary selected for sandbox placement.
- `HermesGateway` means a messaging/channel process belonging to one profile or multiplexing several profiles inside a Hermes sandbox.
- Public APIs, telemetry, configuration, and the workbench use these qualified resource names to prevent ambiguity.

**Gateway registry**

Each registered OpenShell gateway has an immutable identity and versioned metadata for endpoint and trust domain, compute driver, cluster/host, region and failure domain, installation and tenancy scope, isolation level, supported architectures/accelerators, runtime and policy-schema compatibility, provider availability, network/data-locality labels, capacity signals, health, drain state, and certification status.

Credentials used by the DataGround OpenShell adapter are gateway-scoped, short-lived where supported, mTLS-bound, and never exposed to browser or sandbox clients.

**Placement algorithm**

1. Resolve the requested service revision, runtime matrix, environment, isolation mode, organization/tenant and team policy, data residency, provider, accelerator, network, and availability requirements.
2. Hard-filter gateways that cannot enforce or host every required constraint.
3. Rank eligible gateways using health, declared capacity, current pressure, locality, failure-domain spread, affinity/anti-affinity, maintenance state, and operator preference.
4. Acquire an idempotent placement reservation and ask the selected OpenShell gateway to create the sandbox.
5. Bind the resulting sandbox identity, gateway, effective policy hash, compute driver, and runtime matrix into the DataGround execution record.
6. Release or reconcile the reservation after success, failure, timeout, cancellation, or lost acknowledgement.

Operators may pin a revision, workspace, team, or organization/tenant to one gateway or an allowed gateway set. A gateway may be shared, team-dedicated, tenant-dedicated, environment-specific, or reserved for a compute/isolation class.

Gateway placement is immutable for a running sandbox. Moving work to another gateway uses ADR-022's checkpoint-and-recreate migration with validation and rollback; it is not a live reassignment of the same OpenShell sandbox.

**Development and failure behavior**

- Local development registers one Docker/Podman or local-Kubernetes OpenShell gateway and exercises the same selection contract without requiring a distributed scheduler.
- A drained gateway receives no new placements; existing work follows declared drain or migration policy.
- Gateway unavailability does not imply that another gateway can adopt its running sandboxes. DataGround reconciles their state and restores or resubmits recoverable work through an eligible gateway.
- If no gateway satisfies all hard constraints, admission fails or queues with an explainable placement result; DataGround never silently weakens isolation, residency, policy, or runtime requirements.

**Exit gate**

Placement tests cover one and many gateways, heterogeneous drivers, local development, capacity exhaustion, policy/version mismatch, tenancy pinning, data locality, drain, concurrent reservations, gateway loss, migration, and no-eligible-gateway cases without duplicate sandboxes or weakened constraints.

**Validation basis**

- [OpenShell gateway management and multiple gateways](https://docs.nvidia.com/openshell/sandboxes/manage-gateways)
- [How OpenShell gateways and sandboxes work](https://docs.nvidia.com/openshell/about/how-it-works)
- [OpenShell support matrix and compute drivers](https://docs.nvidia.com/openshell/reference/support-matrix)

### ADR-033: Apache Iceberg REST is the catalog contract

**Status:** Confirmed
**Resolves:** Specification decision 8, Catalog

DataGround shall use the Apache Iceberg REST Catalog protocol as the engine-facing catalog boundary. The first release ships and certifies one self-hosted implementation while preserving compatibility with conforming replacements. The concrete server remains a release/deployment choice and is not embedded in public DataGround APIs.

**Required consequences**

- Spark, Trino, PyIceberg, and other supported engines integrate through the pinned Iceberg REST protocol and table-format compatibility matrix rather than implementation-specific client libraries.
- DataGround owns product authorization, workspace/tenant mapping, policy decisions, lineage, publication workflow, and audit. The catalog owns Iceberg namespace/table metadata and commit semantics; neither silently substitutes for the other.
- Browser clients receive DataGround data-resource APIs, not unrestricted catalog credentials or raw catalog administration endpoints.
- Engine and sandbox access uses short-lived, scoped workload identity or brokered credentials bound to catalog namespace/table actions and the required storage locations.
- The certified implementation must pass protocol conformance, optimistic-concurrency, atomic table-commit, concurrent writer, namespace isolation, credential, failover, backup/restore, and upgrade tests.
- Implementation-specific features are optional declared extensions. A published table or service cannot require an extension without recording that portability constraint.

**Exit gate**

The same governed Iceberg tables can be created, committed, read, evolved, and rolled back through the certified catalog using every supported compute engine, and the catalog implementation can be replaced in a conformance environment without changing DataGround resource APIs.

**Validation basis**

- [Apache Iceberg REST Catalog specification](https://iceberg.apache.org/rest-catalog-spec/)
- [Apache Iceberg table specification](https://iceberg.apache.org/spec/)
- [Apache Iceberg reliability and atomic commits](https://iceberg.apache.org/docs/1.6.0/reliability/)

### ADR-034: Governed data publication uses staging and control-plane commit

**Status:** Confirmed initial direction
**Resolves:** Specification decision 33, Data publication

A sandbox shall not acquire standing authority to write arbitrary production table metadata or canonical object prefixes. Governed publication is a two-stage operation:

1. The sandbox writes candidate data files, validation evidence, and provenance into a run-scoped staging location using a narrow OpenShell-governed storage capability.
2. The DataGround control plane validates and atomically publishes the accepted files through the Iceberg REST catalog under a separately authorized publication identity.

**Publication request**

The immutable request identifies organization/tenant, team, workspace, source execution, principal and service identity, target namespace/table, operation mode, expected base snapshot, schema and partition specification, staged files and checksums, statistics, data classification, lineage, quality results, retention, and idempotency key.

**Validation and commit**

- Cedar authorizes the publication action and target independently from the sandbox's ability to create staging files.
- DataGround verifies that every referenced object belongs to the authorized staging grant, has an accepted immutable format/checksum, and is not already bound to an incompatible publication.
- Schema compatibility, partition/sort requirements, table properties, classification, ownership, lineage, quality gates, quotas, and expected snapshot are checked before commit.
- The publisher commits through the catalog using optimistic concurrency and an atomic Iceberg table update. A conflict refreshes and revalidates or fails explicitly; it never overwrites an unexpected snapshot.
- Idempotent retries return the original publication result or resume cleanup without creating duplicate table snapshots.
- Failed, rejected, abandoned, and superseded staging objects follow an auditable lifecycle and garbage-collection policy. They cannot become discoverable production data merely because they exist in object storage.
- A single-table publication receives Iceberg's atomic commit guarantees. Multi-table workflow atomicity is not implied and requires an explicit orchestration contract.

Artifacts remain ordinary governed artifacts until an authorized publication creates or updates a dataset/table resource. Publication records retain source execution, policy revision, catalog snapshot, file set, schema, lineage, and validation evidence.

**Exit gate**

Acceptance tests prove successful append/create/replace operations, concurrent conflict handling, unauthorized target and file injection denial, schema and quality rejection, idempotent retry, publisher crash recovery, orphan cleanup, snapshot rollback, and catalog/storage authorization consistency.

**Validation basis**

- [Apache Iceberg REST Catalog specification](https://iceberg.apache.org/rest-catalog-spec/)
- [Apache Iceberg transaction API](https://iceberg.apache.org/docs/nightly/api/)
- [Apache Iceberg reliability and atomic commits](https://iceberg.apache.org/docs/1.6.0/reliability/)

### ADR-035: S3 compatibility is the durable object-storage contract

**Status:** Confirmed
**Resolves:** Specification decision 7, Storage

DataGround shall use the S3 API as the canonical durable object-storage contract for both platform objects and lakehouse objects. The first release certifies one self-hosted S3-compatible implementation while allowing conforming self-hosted or managed replacements.

This is protocol consolidation, not storage-domain consolidation. Platform objects and lakehouse objects remain separately governed and may be placed on the same physical implementation or on entirely separate storage systems.

**Storage classes**

| Class | Representative contents | Contract and lifecycle |
| --- | --- | --- |
| Sandbox runtime storage | Working directories, temporary files, caches, local indexes, materialized profile homes | Provisioned by the selected OpenShell gateway and compute driver; ephemeral or explicitly checkpointed; no S3-compatibility requirement |
| Platform durable objects | Artifacts, notebook revisions, service outputs, Hermes profile exports, sandbox checkpoints, large event/output segments, retained logs | S3-compatible object API behind DataGround artifact/checkpoint services |
| Lakehouse objects | Iceberg metadata, manifests, data/delete files, staged publications, table-maintenance outputs | S3-compatible object API governed through catalog, publication, and engine identities |
| Relational state | Identities, workspaces, service revisions, jobs/runs, grants, policy references, delivery indexes | PostgreSQL; outside the object-storage contract |

**Isolation and naming**

- Platform and lakehouse classes use distinct buckets or equivalent administrative containers, access points, credential/provider profiles, lifecycle rules, encryption/key references, replication, backup, audit, and quota policy.
- Organization/tenant, team, workspace, data-class, environment, and residency boundaries are represented in allocation policy and identifiers; user-controlled object names cannot choose or escape an administrative prefix.
- Sharing one physical S3-compatible installation never grants a platform artifact identity access to lakehouse objects or vice versa.
- Direct browser access uses narrowly scoped, short-lived signed or proxied operations issued by the owning DataGround service. Root or broadly reusable bucket credentials are never exposed.
- Sandboxes receive only run-scoped object capabilities mediated by OpenShell provider and network policy. The capability binds allowed bucket/prefix, operation class, lifetime, and purpose.

**Portability contract**

- DataGround defines and tests the required S3 subset, including object operations, multipart upload, conditional requests, checksums, range reads, pagination, versioning expectations, server-side encryption behavior, and event/consistency assumptions.
- Features outside that subset are implementation-specific extensions and cannot become undeclared dependencies of portable services or tables.
- Endpoint style, region, TLS trust, path-style compatibility, signing, encryption integration, and implementation-specific tuning remain deployment configuration.
- The certified self-hosted implementation and every supported replacement run the same artifact, checkpoint, Iceberg, concurrent-write, failure, recovery, lifecycle, and authorization conformance suite.

**Operational separation**

An installation may use:

- one S3-compatible cluster with strongly separated administrative domains;
- separate platform-object and lakehouse clusters;
- further separation by organization/tenant, region, sensitivity, or environment; or
- conforming managed object stores in a hybrid deployment.

These choices do not change DataGround resource APIs, catalog contracts, artifact identifiers, or publication semantics.

**Exit gate**

The same release passes its storage conformance suite with the certified self-hosted backend and at least one replacement configuration. Cross-class, cross-prefix, cross-tenant, expired-grant, path-confusion, incomplete-multipart, concurrent-write, restore, and backend-migration tests fail safely without object loss or authorization widening.

### ADR-036: Immutable OCI images are the environment contract

**Status:** Confirmed
**Resolves:** Specification decision 10, Environment model

DataGround environments shall resolve to immutable OCI image digests plus a versioned environment manifest. Nix closures are not a second runtime or distribution contract. Nix may be offered as an optional reproducible build backend that produces a conforming OCI image, but OpenShell gateways and service revisions consume the OCI artifact and its DataGround metadata.

**Environment manifest**

The immutable manifest records:

- environment identifier, revision, owner, source template, and build provenance;
- OCI image reference and digest for every supported architecture;
- base-image lineage, operating-system packages, language/package-manager lockfiles, harnesses, adapters, kernels, SDKs, and toolchains;
- entrypoints and supported workload classes;
- required and optional DataGround capabilities;
- compatible OpenShell, policy-schema, Rosetta, runtime-matrix, driver, architecture, and accelerator constraints;
- filesystem layout and declared working, cache, artifact, and checkpoint paths;
- SBOM, vulnerability and license results, signatures/attestations, build logs, tests, and certification state;
- default resources, health checks, startup semantics, and known limitations.

**Build and execution rules**

- A published service revision binds an exact environment revision and image digest. Mutable tags are display aliases only and are never the execution identity.
- Production startup does not install undeclared packages from the network. Package changes create a new environment build and revision.
- Development may permit an explicitly non-reproducible interactive overlay, but it is marked dirty and cannot be promoted until captured, rebuilt, scanned, tested, and pinned.
- Environment builds run as governed jobs with isolated credentials, source inputs, network policy, cache provenance, resource limits, and signed outputs.
- The build implementation is replaceable. Dockerfile/BuildKit, Nix, language-native lockfiles, or another builder may be accepted inputs if the resulting OCI image and evidence satisfy the same contract.
- Local Docker, local Kubernetes, and self-hosted production use the same image digest. Deployment configuration and secret/provider bindings remain external.
- Multi-architecture images are certified per architecture; a manifest list alone does not prove equivalent behavior.

**Nix boundary**

When Nix is used, DataGround records the flake/derivation inputs and closure provenance as build evidence. The closure is copied or layered into the OCI image. DataGround does not require OpenShell gateways, Kubernetes nodes, or sandbox clients to understand Nix store identities, and a service revision does not choose between an OCI identity and a Nix identity.

**Exit gate**

The same environment digest starts successfully through local and production-reference gateways, passes harness and policy conformance, reproduces from declared inputs within the documented reproducibility boundary, and rejects mutable, unsigned, incompatible, vulnerable-beyond-policy, or architecture-mismatched images.

### ADR-037: Dedicated Databricks migration is deferred

**Status:** Deferred by product priority
**Tracks:** Specification decision 11, Databricks migration

Databricks migration is not an initial DataGround concern. The platform shall not delay its architecture or first release to implement a Databricks workspace importer, REST compatibility layer, UI clone, or automated semantic conversion.

This deferral does not justify proprietary lock-in. DataGround continues to use and preserve open interchange where it has independent product value, including `.ipynb`, Apache Iceberg, Parquet, MLflow artifacts/metadata, S3-compatible objects, OIDC, OpenAPI, OpenTelemetry, and exportable service/environment manifests.

**Initial boundary**

- Ordinary notebook import/export and open-format data access are platform features, not a dedicated Databricks migration program.
- Unsupported Databricks notebook directives, cluster policies, job semantics, Unity Catalog behavior, secrets, mounts, libraries, and proprietary APIs are not silently approximated.
- No Databricks API-compatibility promise appears in public contracts or acceptance gates.
- Architecture decisions may preserve clean adapter points where inexpensive, but speculative migration abstractions do not enter the critical path.

**Revisit trigger**

A future migration ADR requires a concrete customer or commercial priority, representative exported workspaces, target asset classes, acceptable semantic loss, security and identity mapping, success metrics, and ownership. It should prefer evidence-based import/export tools over broad protocol emulation unless actual demand demonstrates otherwise.

### ADR-038: Platform-owned PostgreSQL reconcilers precede a general workflow engine

**Status:** Confirmed
**Resolves:** Specification decision 9, Workflow engine

The initial DataGround release shall not require Temporal, Argo Workflows, or Apache Airflow as the durable source of truth for platform lifecycle orchestration. DataGround shall implement a bounded set of explicit resource state machines and reconciliation loops over its existing PostgreSQL control-plane state and transactional event/outbox contract.

This decision minimizes the mandatory self-hosted footprint while preserving correct recovery across local Docker, local Kubernetes, self-hosted production, and one or many OpenShell gateways. It does not authorize DataGround to build a general-purpose workflow language.

**Initial platform-owned state machines**

- sandbox placement, provisioning, health, drain, teardown, and migration;
- service/environment publication, validation, rollout, rollback, and retirement;
- interactive session and ordinary job/run lifecycle;
- artifact finalization and governed data publication;
- durable callback delivery and dead-letter/replay lifecycle;
- Hermes sandbox/profile restore, hibernation, channel supervision, and migration;
- retention, deletion, checkpoint, backup-verification, and recovery operations.

Each state machine has a versioned finite state set, accepted commands, transition preconditions, terminal states, retry classification, timeout behavior, compensation/cleanup rules, and auditable invariants. Product APIs expose DataGround resources and operations rather than internal worker tasks.

**Durability and concurrency contract**

- Desired state, observed state, state-machine version, generation, last transition, operation identifier, attempt, lease/ownership, next reconciliation time, error classification, and terminal result are durable PostgreSQL records.
- A transaction that accepts a command or commits a transition also writes the corresponding outbox events. State cannot advance without its externally observable event being durably scheduled.
- Incoming commands, callbacks, and provider observations carry stable idempotency/deduplication identifiers. Reprocessing produces the existing result or safe continued reconciliation.
- Workers are replaceable and hold no exclusive durable workflow state in memory. Leases expire and can be reclaimed after crashes.
- External side effects use deterministic operation identifiers and are observed after ambiguous timeout or lost acknowledgement before being repeated.
- Timers are durable `due_at`/deadline state, not process-local sleeps. Cancellation and administrative termination are state-machine commands with explicit propagation and cleanup.
- Reconciliation is tenant-aware and fair; a failing or hot resource cannot monopolize the shared queue or database.
- State transitions, external attempts, policy revisions, actors, outcomes, and compensations are correlated into audit and OpenTelemetry signals without copying sensitive payloads unnecessarily.

**Execution backends**

Argo Workflows, native Kubernetes operators, Spark operators, or equivalent systems may be optional execution adapters for container/data DAGs. They do not become the authoritative record for DataGround jobs, services, permissions, artifacts, approvals, or lifecycle state. The adapter maps a DataGround execution to the external system, observes it idempotently, and reconciles the normalized result.

Airflow may be integrated as an external scheduler or customer-owned workflow system through published APIs and callbacks. DataGround does not require operators to deploy Airflow for core platform operation.

**Scope guardrails**

- No user-authored arbitrary workflow code executes inside control-plane reconcilers.
- No general DAG DSL, workflow SDK, deterministic replay runtime, or competing scheduler is created in the initial release.
- Resource state machines share infrastructure primitives but retain explicit domain ownership and schemas; one generic `workflow` table does not erase lifecycle semantics.
- Complex data pipelines remain jobs/workflows submitted to an execution adapter rather than expanding the control plane into a data scheduler.
- Schema and state-machine upgrades include forward migration, mixed-version behavior, rollback constraints, and stuck-state repair procedures.

**Temporal revisit trigger**

Temporal or another durable-execution engine is reconsidered when evidence shows repeated platform implementations of long-lived human interaction, cross-domain compensation, durable signal routing, child-workflow composition, versioned replay, or timer/state logic that cannot remain small and auditable. The evaluation must compare measured engineering and incident cost with the full self-hosted operational footprint, persistence, upgrade, security, and developer-model cost.

**Exit gate**

Failure-injection tests terminate workers and control-plane instances before and after every durable transition and external side effect. The system must resume without lost commands, duplicated sandboxes/publications/callback effects, stuck leases, unauthorized transitions, or cross-tenant starvation. The same conformance suite runs in local Docker and the production Kubernetes reference profile.

**Validation basis**

- [Kubernetes controller and reconciliation model](https://kubernetes.io/docs/concepts/architecture/controller/)
- [Temporal durable execution](https://docs.temporal.io/temporal)
- [Temporal self-hosted production checklist](https://docs.temporal.io/self-hosted-guide/production-checklist)
- [Argo Workflows concepts](https://argo-workflows.readthedocs.io/en/latest/workflow-concepts/)
- [Argo workflow archive](https://argo-workflows.readthedocs.io/en/latest/workflow-archive/)
- [Apache Airflow architecture](https://airflow.apache.org/docs/apache-airflow/stable/core-concepts/overview.html)

## Decision backlog and order

| Batch | Decisions | Why this order matters |
| --- | --- | --- |
| 6c. Runtime integration contract | Rosetta API | Freezes the remaining external compiler boundary. |
| Deferred product work | 11 Databricks migration | Revisit only with concrete demand and representative migration inputs. |

## Current implementation-freeze status

- Confirmed specification decisions in this register: 33
- Confirmed additional product/security/runtime decisions: 3
- Confirmed dependency with a remaining contract sub-decision: 1
- Unaddressed specification decisions: 0
- Deferred product-priority decisions: 1
- Remaining implementation-freeze dependency: Rosetta API contract
