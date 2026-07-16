# Source of truth and artifact inventory

## Normative order

When artifacts disagree, use this order:

1. Confirmed ADR-001 through ADR-038.
2. DataGround system specification, Draft 0.4.1.
3. Signed release certification manifest for exact versions, digests, schemas, capability profiles, and measured capacity.
4. Versioned public API and event schemas.
5. Implementation plans and the implementation starter.

An implementation prompt, test, generated file, or UI mockup cannot silently override a higher-ranked source.

## Authoritative project artifacts

| Artifact | Role | Status |
| --- | --- | --- |
| [`../architecture/decision-register.md`](../architecture/decision-register.md) | ADR-001–038 rationale, consequences, and exit gates | Normative for decisions |
| [`../architecture/system-specification.md`](../architecture/system-specification.md) | Product, security, runtime, data, API, operational, and acceptance contracts | Normative Draft 0.4.1, subordinate to confirmed ADRs |
| [`README.md`](README.md) and sibling starter documents | Prerequisites, implementation sequence, proposed choices, and verification guidance | Informative implementation guidance |

## Confirmed architecture that implementation must preserve

- Agent services are the first end-to-end MVP.
- Codex, Claude Code, OpenCode, and Hermes are first-class initial runtime families.
- The platform-native service and event contracts precede OpenAI compatibility.
- Native runtime protocols remain internal; ACP is a possible later northbound facade.
- Cedar expresses authorization intent; Rosetta is the external Cedar-to-OpenShell compiler.
- Real provider credentials never become visible inside notebook or harness processes.
- OpenShell gateways are registered placement targets; DataGround selects the gateway.
- PostgreSQL-backed resource state machines and a transactional outbox precede any general workflow engine.
- S3 compatibility, Iceberg REST, immutable OCI images, and OpenTelemetry are stable boundaries.
- The platform is self-hosted-first, supports local Docker/Podman and local Kubernetes, and targets conforming Kubernetes for production.
- Tenancy is optional in the user experience but isolation scoping is mandatory in identifiers, policy, caches, queues, storage, telemetry, and audit.

The specification already incorporates the ADR-003 agent-service-first sequence and the ADR-019 Rosetta boundary. If later edits reintroduce conflicting proposal language, the confirmed ADR remains authoritative.

## Evidence classifications

Every implementation claim should be tagged as one of:

| Class | Meaning | May ship? |
| --- | --- | --- |
| Confirmed | Required by the specification or a confirmed ADR | Yes, when verified |
| Release input | Exact version, digest, capacity, retention, provider, or infrastructure value | Only after pinned in the release manifest |
| Proposed | Recommended implementation choice awaiting confirmation or spike evidence | Prototype only |
| External blocker | Contract controlled outside DataGround | Only fail-closed scaffolding may ship |
| Deferred | Explicitly outside the initial critical path | No accidental partial product |

## Change control

- Architecture changes require a new ADR; do not edit a confirmed ADR's outcome in place.
- API or event changes require schema versioning and compatibility notes.
- Exact dependency versions belong in the release manifest, not this guidance.
- Generated clients and schemas are committed or reproducibly generated in CI; drift fails CI.
- Every accepted change names the source requirement and its verification evidence.
