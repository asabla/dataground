# Build readiness and prerequisites

## Readiness classification

| Area | Status | Required action |
| --- | --- | --- |
| Product and architecture | Ready | Preserve ADR-001–038 |
| First vertical slice | Ready | Use the agent-service slice in `first-vertical-slice.md` |
| Rosetta policy materialization | Blocked externally | Freeze client contract when Rosetta publishes it; remain fail-closed |
| Repository | Partially ready | Project layout and CI exist; choose licensing, ownership and release policy |
| Frontend | Design-system ready | React/Vite shell, generated API types, semantic tokens and deterministic interaction fixtures exist; router, auth and Jupyter contracts remain open |
| Implementation stack | Contract-core ready | Go control plane, deterministic reference runtime and TypeScript/React workbench are established; runtime sidecars remain evidence-driven |
| Reference S3 and Iceberg implementations | Evidence spike required | Select one certified self-hosted implementation of each |
| Exact runtime versions | Development profile pinned | Codex 0.117.0 and OpenShell 0.0.86 are pinned for the first blocked profile; complete live certification before release |
| Capacity and SLO numbers | Pilot input required | Define test hardware and measure; do not invent targets |
| Production identity and workload identity | Deployment input required | Select IdP, token exchange, certificate issuer, and trust domains |
| Retention, RPO/RTO, residency | Operator input required | Record per deployment profile before production approval |

## P0: resolve before repository bootstrap freezes

These items materially affect repository boundaries or contracts:

1. **Repository governance:** [`asabla/dataground`](https://github.com/asabla/dataground), its `main` branch and GitHub Actions baseline are established. Confirm ownership, license, release process and contribution policy.
2. **Frontend integration:** React, Vite, TypeScript and the initial semantic-token shell are established. Confirm router/state/data layers, Jupyter transport, editor/notebook packages, authentication and hosting mode before product UI work.
3. **Backend language:** Go is established for the platform core. Add TypeScript or Python runtime sidecars only where a confirmed upstream SDK and conformance evidence justify the process boundary.
4. **Package and build tooling:** pnpm, Biome, Vitest, Go modules, reproducible OpenAPI type generation and the root `pnpm verify` contract are established. Container building and local orchestration remain open until their consumers exist.
5. **Initial public contract:** resource names, isolation-scoped URL conventions, error envelope, idempotency, event envelope, SSE replay and schema compatibility policy are established. Pagination is added with the first collection read surface.
6. **Initial reference runtime:** the deterministic fake and conformance fixtures are established. Choose the first real adapter used after it; this does not change the requirement to certify all four runtime families for initial release.
7. **Local OpenShell topology:** OpenShell 0.0.86, the Docker driver, loopback gateway, immutable images, deny-all fixture, and Codex app-server stdio are pinned under `deploy/openshell`. Live service routing and credential non-exposure certification remain blocked on a Docker-capable host and mediated provider setup.

Do not ask Codex to generate a full platform before the remaining items are known. Prompt `00` records the bootstrap contract; prompt `01` starts product contracts only after its required decisions are resolved.

## P0: resolve before a real sandbox is allowed to perform work

- Pin the OpenShell release and gateway/supervisor images by digest.
- Capture its supported policy schema, API/CLI contract, service-routing behavior, compute-driver claims, and credential mediation behavior.
- Define the `ExecutionProvider` port and run its contract suite against the pinned gateway.
- Provide an externally generated, immutable development enforcement bundle or deny-all fixture. Mark it non-production and bind it to a hash.
- Prove the browser receives no gateway URL, sandbox port, Kubernetes credential, root object-store credential, or upstream runtime endpoint.
- Prove a harness process cannot inspect a raw provider key or refresh token.

## Rosetta freeze gate

The Rosetta client can be scaffolded now. The following are required before its production integration freezes:

- supported Cedar subset and schema versions;
- request/entity/input schema;
- target OpenShell versions and policy fields;
- deterministic output and canonicalization rules;
- validation/dry-run endpoint;
- error taxonomy and safe diagnostics;
- idempotency and retry semantics;
- provenance, input/output hashes, and compiler identity;
- compatibility rules and deprecation window;
- signed conformance fixtures for permit, deny, unrepresentable, and invalid cases.

Until then, `RosettaClient.materialize` must return `UNAVAILABLE` or use explicit test fixtures. No fallback compiler is permitted.

## Reference implementation selection spikes

Run bounded, scored spikes rather than selecting by popularity.

### S3-compatible object storage

Score license, S3 conformance for required operations, versioning, object lock/retention, encryption and KMS integration, multipart behavior, presigned operations, replication, backup/restore, Kubernetes operation, observability, upgrade history, and multi-architecture support.

### Iceberg REST catalog

Score protocol conformance, namespace/table operations, optimistic concurrency, credential vending model, authorization integration, S3 interoperability, Spark/Trino compatibility, migrations, HA, backup/restore, observability, release stability, and license.

The selected product is replaceable; the conformance fixture is the durable asset.

## Deployment inputs required before production approval

- isolation profile: Trusted installation, Team-isolated, or Hostile multi-tenant;
- policy administration: Owner-managed, Reviewed, or High-assurance;
- runtime escalation default: Locked, Semi-interactive, or Auto within a parent ceiling;
- identity provider, issuer/audiences, group mapping, service-principal lifecycle;
- internal mTLS and workload credential issuer;
- object and database backup/restore targets;
- content, artifact, event, audit, and backup retention;
- data residency and allowed gateway regions;
- hardware inventory, GPU types, node pools, storage/network shape;
- measured Developer, Team, and Production capacity envelopes;
- RPO, RTO, availability and recovery ownership;
- vulnerability, license, signature, and emergency revocation policy.

## Safe work that can begin immediately

- schemas and contract tests;
- deterministic fake runtime;
- PostgreSQL resource/state/outbox schema;
- service/revision/deployment/invocation/event APIs;
- event replay and cancellation semantics;
- audit vocabulary and OpenTelemetry correlation fields;
- workbench shell, tokens, accessible primitives, and interaction viewer against fixtures;
- release-manifest schema;
- adapter and driver conformance harnesses;
- threat model and failure-injection harness.
