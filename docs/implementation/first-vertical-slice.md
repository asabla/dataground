# First vertical slice: governed agent service

## Outcome

One authorized user can create an agent service, publish an immutable revision, route a stable alias, invoke it through the native API, observe and replay typed events, cancel it, retrieve outputs/artifacts, inspect usage and audit, and retire the revision without seeing an OpenShell or upstream runtime endpoint.

The slice is complete first against a deterministic fake runtime, then against one pinned real runtime in a local OpenShell sandbox. Initial-release certification still requires Codex, Claude Code, OpenCode, and Hermes.

## Why this slice comes first

It proves the platform's hardest reusable contracts before notebook and lakehouse breadth:

- identity, scoping, and authorization;
- immutable revisions and aliases;
- durable desired/observed state;
- event-first interaction and replay;
- adapter capability negotiation;
- cancellation, idempotency, retries, and audit;
- artifact finalization;
- gateway placement without leaking infrastructure;
- UI patterns for long-running, partially interactive work.

## Slice resources

Use explicit domain resources, not a generic workflow table:

- `Workspace`
- `AgentService`
- `ServiceRevision`
- `ServiceAlias`
- `ServiceDeployment`
- `Invocation`
- `Operation`
- `EventRecord`
- `Artifact`
- `AuditRecord`
- `RuntimeProfile`
- `GatewayRegistration`

Every resource identifier includes or resolves to an isolation-domain identifier. Stable public IDs do not expose database sequence, gateway, sandbox, or upstream runtime IDs.

## Milestone sequence

### M0 — executable repository

- Monorepo builds from a clean checkout.
- Local dependencies start reproducibly.
- CI runs format, lint, unit, schema, migration, license, secret, and build checks.
- Root `AGENTS.md` contains only verified commands and constraints.

### M1 — contract core and deterministic runtime

- Versioned OpenAPI plus JSON Schemas for resources, commands, errors, and events.
- Fake runtime replays deterministic success, question, approval, artifact, cancellation, failure, duplicate, and out-of-order fixtures.
- PostgreSQL stores resources, operations, event journal, audit records, idempotency records, leases, and transactional outbox.
- REST commands and SSE replay work end to end.
- UI renders the same event fixtures used by backend tests.

### M2 — bounded reconciler

- Service publication and invocation use explicit state machines.
- Workers are replaceable; leases and durable timers recover after process termination.
- External effects use deterministic operation IDs.
- Failure injection before and after every durable transition produces no lost command or duplicate publication/invocation.

### M3 — local OpenShell execution

- Register one local gateway and select it by explicit placement constraints.
- Launch one runtime behind the `ExecutionProvider` boundary.
- Use an externally supplied development enforcement fixture or deny-all policy.
- Stream normalized runtime events without exposing its native protocol.
- Persist declared outputs and artifacts before teardown.

### M4 — workbench MVP

- Resource/scope navigation.
- Service create/revision/publish flow.
- Invocation composer driven by input schema.
- Event timeline with text, tool, process, file, question, approval, usage, lifecycle, warning, and error events.
- Controller/observer distinction, cancellation, reconnect, replay, and artifact inspection.
- Policy/capability explanation and revision provenance.

### M5 — adapter matrix and release evidence

- All four runtime families run the shared conformance kit.
- Missing optional capabilities are explicit; missing required capabilities block publication.
- Runtime versions, digests, schemas, provider profiles, capability results, and migration compatibility are pinned in a release manifest.
- Local Docker/Podman and local Kubernetes run the same slice tests.

## Native API minimum

The exact schema is produced in the implementation repository, but the minimum behavior is:

- create/update service draft;
- create immutable revision;
- validate and publish revision;
- create/inspect deployment;
- assign/move stable alias with optimistic concurrency;
- invoke synchronously or asynchronously;
- list/read/replay invocation events from a cursor;
- cancel invocation idempotently;
- retrieve result, usage, and artifact metadata;
- inspect operation and audit correlation;
- retire revision after traffic and retention checks.

Every mutating request accepts an idempotency key. Errors use the stable envelope from the specification. SSE is authoritative before WebSocket steering is added.

## Event envelope invariants

- Globally unique event ID and invocation-local monotonic sequence.
- Stable schema version, event type, occurred/recorded times, correlation IDs, actor/service identity, and safe payload.
- Resume cursor refers to the persisted journal, not process memory.
- Large or sensitive payloads become governed artifact references.
- Duplicate delivery is allowed; conflicting content for one event ID is not.
- Provider-specific detail is namespaced and optional; clients can ignore it.
- Redaction occurs before persistence and telemetry export.

## Non-negotiable security checks

- Cross-domain IDs cannot collide in authorization, cache, event replay, artifacts, or audit.
- A platform operator without content permission cannot inspect payloads.
- Browser and caller never receive raw sandbox/provider endpoints or credentials.
- Runtime process inspection finds no raw provider secret.
- Approval UI never claims a grant that Cedar plus enforcement cannot represent.
- An unavailable Rosetta integration fails closed.
- Cancellation and teardown revoke short-lived grants and stop new external effects.

## Definition of done

The slice passes the contract, integration, security, resilience, accessibility, and local-profile gates in `docs/implementation/verification-and-release-gates.md`; produces a signed release manifest and SBOM; and has an operator runbook for install, backup/restore, stuck-state repair, gateway drain, runtime rollback, and incident evidence collection.
