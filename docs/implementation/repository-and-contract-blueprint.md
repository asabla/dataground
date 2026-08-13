# Repository and contract blueprint

## Proposed implementation shape

This is a recommended default, not a confirmed ADR:

- Go for control-plane APIs, PostgreSQL reconcilers, gateway selection, event/outbox workers, and infrastructure adapters.
- TypeScript/React for the workbench and design-system packages.
- Thin TypeScript or Python sidecars only where an upstream runtime SDK makes that the lowest-maintenance native integration.
- Protobuf is not required for public contracts. Start with OpenAPI 3.1 and JSON Schema 2020-12 for REST/events; add an internal RPC IDL only when a measured need exists.

The objective is not language purity. It is a stable platform core plus very small, replaceable upstream-specific processes.

The executable bootstrap implements the smallest subset of this shape: `cmd/dataground-api`, `internal`, `apps/workbench`, `contracts`, and `scripts`. The remaining directories are created only when their first verified consumer exists.

## Monorepo layout

```text
/
  AGENTS.md
  PLANS.md
  README.md
  docs/
    architecture/
    adr/
    runbooks/
    threat-model/
  contracts/
    openapi/
    schemas/
    events/
    fixtures/
    generated/
  apps/
    workbench/
    api/
    worker/
    callback-dispatcher/
  services/
    identity/
    policy/
    artifact/
    harness-gateway/
  adapters/
    execution-openshell/
    runtime-reference/
    runtime-codex/
    runtime-claude/
    runtime-opencode/
    runtime-hermes/
    catalog/
    object-store/
  packages/
    domain/
    persistence/
    telemetry/
    testkit/
    tokens/
    ui/
    patterns/
    icons/
  deploy/
    compose/
    local-kubernetes/
    kubernetes/
  release/
    manifests/
    compatibility/
  scripts/
```

Do not create deployable services merely to match this directory list. Begin as a modular monolith plus replaceable workers and adapter processes. Split a module only when it needs a distinct trust boundary, scaling profile, failure domain, release cadence, or upstream runtime.

## Contract ownership

| Contract | Owner | Compatibility rule |
| --- | --- | --- |
| Public REST resources/commands | API module | Additive within major version; explicit removal window |
| Event envelope and event types | Event module | Unknown types/fields are ignorable; semantic changes require version |
| Error codes | API platform | Stable meaning; safe message is not machine logic |
| Runtime adapter protocol | Harness gateway | Capability negotiated; recorded fixtures per upstream version |
| Execution provider protocol | Lifecycle module | Idempotent operations and observed-state recovery |
| Rosetta client | Policy module | Fail closed; pinned schema and conformance corpus |
| Object/catalog interfaces | Data modules | Protocol conformance, not vendor behavior |
| Release certification manifest | Release engineering | Immutable, signed, reproducible |

## First schemas to freeze

1. Resource metadata: ID, scope, generation, version, labels, created/updated, actor and provenance.
2. Error envelope: code, safe message, correlation ID, retryable, field errors.
3. Operation: command, desired/observed state, attempts, lease, due/deadline, classification and terminal result.
4. Agent service/revision/alias/deployment.
5. Invocation input/output and cancellation.
6. Event envelope and initial event types.
7. Artifact descriptor and finalization state.
8. Runtime capability manifest.
9. Gateway registration/capability/placement explanation.
10. Release certification manifest.

## State-machine implementation rules

- Domain packages define finite states and allowed transitions.
- PostgreSQL is authoritative; no process-local workflow state.
- A command/transition and its outbox records commit in one transaction.
- Workers claim bounded leases with fencing tokens or equivalent stale-owner protection.
- External calls use deterministic operation identifiers and observe before retrying ambiguous outcomes.
- Backoff is persisted as `due_at`; workers do not hold durable sleeps.
- Repair commands are explicit, authorized, audited, and safe to repeat.
- Migrations declare mixed-version behavior and rollback constraints.

## Test kits as first-class products

Build these before broad implementations:

- `runtime-conformance`: normalized lifecycle, events, questions, approvals, artifacts, cancellation, resume, usage and capability drift.
- `execution-provider-conformance`: provisioning, idempotency, policy attachment, routing, logs, file/artifact movement, loss, drain and cleanup.
- `event-conformance`: order, duplicates, reconnect, cursor expiry, redaction and large-payload spill.
- `isolation-conformance`: cross-domain identifier, cache, storage, queue, telemetry and audit separation.
- `release-conformance`: digests, signatures, SBOM, schema capture, migrations, rollback and certified capacity evidence.

## CI minimum

- deterministic generation check and clean working tree after generation;
- formatting, lint, type checks, unit and contract tests;
- database migration up/down or forward/rollback-policy checks;
- OpenAPI/JSON Schema lint and breaking-change detection;
- secret, dependency, license, source and container scans;
- SBOM and provenance generation;
- component accessibility and interaction tests;
- integration tests with deterministic runtime on every change;
- local OpenShell and Kubernetes conformance on protected/nightly lanes;
- failure-injection and restore tests on release candidates.
