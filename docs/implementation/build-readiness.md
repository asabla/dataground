# Build readiness and prerequisites

## Readiness classification

| Area | Status | Required action |
| --- | --- | --- |
| Product and architecture | Ready | Preserve ADR-001–038 |
| First vertical slice | Ready | Use the agent-service slice in `first-vertical-slice.md` |
| Rosetta policy materialization | Contract candidate available; release blocked externally | Keep the strict candidate client unwired until a tagged build, authenticated transport profile, stable error taxonomy and conformance fixtures are certified |
| Repository | Partially ready | Project layout and CI exist; choose licensing, ownership and release policy |
| Frontend | Design-system ready | React/Vite shell, generated API types, semantic tokens and deterministic interaction fixtures exist; router, auth and Jupyter contracts remain open |
| Implementation stack | Contract-core ready | Go control plane, deterministic reference runtime and TypeScript/React workbench are established; runtime sidecars remain evidence-driven |
| Reference S3 and Iceberg implementations | Development S3 subset under conformance; production selection still required | Preserve the vendor-neutral harness, expand it through ADR-035, certify an authenticated self-hosted S3 implementation plus a replacement, and separately select the Iceberg catalog |
| Exact runtime versions | Development profile pinned | Codex 0.117.0 and OpenShell 0.0.86 are pinned for the first blocked profile; durable provider routing exists, but complete live certification before release |
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
7. **Local OpenShell topology:** OpenShell 0.0.86, the Docker driver, loopback gateway, immutable images, deny-all fixture, and Codex app-server stdio are pinned under `deploy/openshell`. The internal provider has PostgreSQL-backed, isolation-scoped gateway placement and restart recovery. An immutable internal execution plan binds exact environment, enforcement, provider-profile, and runtime-matrix inputs to a service revision. The append-only enforcement catalog records deterministic platform-object routing and Rosetta provenance; the object-backed source caps reads and reverifies size and digest before admission. The S3 REST adapter provides bounded reads and conditional, checksummed single-part writes through an explicit operator-owned transport. Its development suite verifies missing reads, exact create/read, immutable replacement denial, concurrent first-writer-wins behavior, and finalizer recovery against a pinned disposable SeaweedFS 4.40 candidate. Recovery evidence joins that backend with PostgreSQL across storage and database restarts, catalog contention, process loss, both deterministic commit-connection-loss outcomes, controlled promotion of a caught-up physical standby behind a stable loopback URL with three consecutive health confirmations bound to the expected PostgreSQL timeline, crash recovery from an exact durably persisted private route record, bounded readiness-gated conformance supervision with exclusive supervisor and manager ownership, immediate contender rejection, automatic supervisor replacement, manager-loss containment, and manager restart-budget exhaustion with recovery-only controlled replacement, bounded reconnection of one long-lived three-connection pool, atomic convergence after primary loss during an unresolved commit, and explicit stale-primary fencing followed by rewind and read-only rejoin. Fresh processes must converge on or replay one durable record and audit without rewriting the object. This anonymous single-process object topology and manually operated database pair do not verify workload authentication, automatic election or failover, automatic fencing, production process supervision, transaction replay, partition safety, production topology, or the broader ADR-035 contract. Named policy files live only in an exclusive private workspace that reclaims only DataGround-owned crash orphans before admitting new creation. Invocation state-machine version 2 separates governed admission from runtime execution and provides optional start and cancellation drivers that resolve durable scope, require explicit consequential-effect authorization immediately before external changes, and observe persisted execution by operation before any retry. A runtime target becomes readable only after successful admission and binds persisted invocation input, revision runtime settings, and current effect authority. Claimed runtime access and normalized event writes require the exact active lease and fencing token; the reconciler passes that authority only through explicit claim-bound routes, bounded renewal cannot cross the operation deadline, and stale workers cannot append. A single-use runtime-attempt reservation prevents replacement workers from repeating a native turn whose outcome is unresolved; fenced success and terminal-failure completion replay exact canonical results, and deterministic runtime failure terminates with a bounded safe error. An optional claim-bound runtime driver requires a ready admitted execution, maps only the durable target through a runtime-profile contract, authorizes the normalized request, starts one Codex turn after reservation, renews the exact lease, and persists every normalized event before completion. The built-in `codex.app-server/v1` mapper accepts a bounded persisted `prompt` and optional immutable artifact declarations, clones the persisted output schema, and fixes locked approvals without caller-controlled model or working-directory overrides. Declarations are bounded, require unique portable identifiers and clean absolute sandbox paths, and force workspace-write sandboxing before authorization; requests without declarations remain read-only. A successful turn materializes only acknowledged text deltas into a bounded declared result; structured output schemas compile before provider access without external resource loading, each result must be exactly one conforming JSON value, and malformed, non-conforming, or oversized output terminates without a second native turn. An invocation-artifact finalizer conditionally creates bounded immutable platform objects and verifies exact read-back after every write outcome. After a successful native turn and valid declared output, the optional runtime driver exports persisted declarations in order, derives stable isolation-scoped artifact identities, and finalizes sensitive content under the exact renewed claim before attempt success; uncertain export or publication outcomes remain ambiguous and cannot repeat the native turn. Its PostgreSQL catalog atomically binds private object routing, the existing public descriptor, and audit evidence only under the exact live version-2 runtime claim and reserved attempt; exact committed replay is read-only. An opt-in, explicitly size-bounded wrapper over the narrow S3 REST store implements the invocation-artifact object ports with exact deterministic key routing, checksummed writes, safe outcome mapping, and an operator-owned transport. The disposable S3 and PostgreSQL profile exercises both the narrow invocation-artifact transport and composed object-finalizer/catalog acknowledgement recovery under an exact reserved runtime claim; default/public runtime composition and production certification remain open. An opt-in all-or-nothing composition owns the version 2 admission, claim-bound runtime, and cancellation routes; incomplete composition fails before worker startup while unowned effects retain the deterministic fallback. One opt-in invocation authorizer maps those three phases onto a closed provider-independent decision contract, validates complete durable scope before evaluation, preserves phase-specific denial errors, and passes an owned normalized runtime request; Cedar evaluation remains unconfigured. Runtime events remain idempotent through a durable source sequence without replacing the public invocation sequence. Cancellation declares a missing execution safely absent only when no admission effect was prepared; unresolved admission remains retryable. Repair and cancellation persist separate effect principals and correlations without rewriting the original requester; existing version 1 operations retain their original reconciliation. An audited operator command and append-only PostgreSQL source now bind canonical Cedar policies to exact service revisions without wildcard resolution, and completed effect-time Cedar evaluations can be recorded append-only with exact policy provenance, and an explicit single-domain governed development worker can compose that policy and audit boundary with pinned loopback OpenShell admission, claim-fenced Codex runtime execution, cancellation, and artifact finalization; reference mode remains the default, while production backend selection, workload-identity configuration, public policy administration and distribution, audit delivery, acknowledgement, access and retention policy, production/default governed execution, richer runtime capabilities, and certified live service routing remain blocked. Provider credential non-exposure is certified only for the exact pinned development profile. A profile-bound runtime-conformance evidence contract now fixes the broader live cases and capability classifications, but no runtime record has been incorporated.

Do not ask Codex to generate a full platform before the remaining items are known. Prompt `00` records the bootstrap contract; prompt `01` starts product contracts only after its required decisions are resolved.

## P0: resolve before a real sandbox is allowed to perform work

- Pin the OpenShell release and gateway/supervisor images by digest.
- Capture its supported policy schema, API/CLI contract, service-routing behavior, compute-driver claims, and credential mediation behavior.
- Define the `ExecutionProvider` port and run its contract suite against the pinned gateway.
- Provide an externally generated, immutable development enforcement bundle or deny-all fixture. Mark it non-production and bind it to a hash.
- Prove the browser receives no gateway URL, sandbox port, Kubernetes credential, root object-store credential, or upstream runtime endpoint.
- Preserve the incorporated pinned-profile proof that a harness process cannot inspect a raw provider key or refresh token; recertify after any bound profile input changes.

## Rosetta freeze gate

The internal client is pinned to Rosetta candidate commit `320158f1e4a4eea378d82c1527f4a7af5fb9855b`, compiler `1.0.0`, catalog `rosetta/v1`, and OpenShell contract `rosetta/openshell-policy-v1`. It forces strict mode, recomputes input and artifact hashes, validates the generated OpenShell policy independently, binds provenance to one isolation domain and revision or execution, and does not expose upstream diagnostic messages. It is not wired to publication or invocation.

The following are still required before production integration freezes:

- a signed `v1.0.0` tag, immutable container digest, SBOM, and build provenance;
- stable machine-readable service error codes and retry semantics;
- an operator-owned authenticated transport profile using workload identity and mTLS;
- explicit compatibility and deprecation policy;
- signed conformance fixtures for permit, deny, unrepresentable, invalid, target drift, and OpenShell differential cases.

Until then, the client remains an internal conformance boundary and live materialization returns unavailable. No fallback compiler is permitted.

## Reference implementation selection spikes

Run bounded, scored spikes rather than selecting by popularity.

### S3-compatible object storage

Score license, S3 conformance for required operations, versioning, object lock/retention, encryption and KMS integration, multipart behavior, presigned operations, replication, backup/restore, Kubernetes operation, observability, upgrade history, and multi-architecture support.

SeaweedFS 4.40 is the first development candidate for the enforcement-object subset because its upstream release includes the atomic conditional-mutation fix required by DataGround and publishes an Apache-2.0 multi-architecture image. The digest-pinned profile and live CI jobs are evidence for `GetObject`, conditional checksummed `PutObject`, immutable replacement denial, concurrent first-writer-wins behavior, finalizer recovery after a lost write acknowledgement or catalog failure, fail-closed behavior during an object outage, retained-object recovery after storage-container and database restarts, synchronized catalog adoption, commit-ambiguity recovery, controlled PostgreSQL physical-standby promotion behind one stable client URL with repeated health confirmation bound to the expected PostgreSQL timeline, stable-route recovery through a bounded singleton conformance supervisor and independently singleton outer manager after router, supervisor, or manager process loss, including immediate contender rejection, automatic supervisor replacement, controlled manager recovery, and manager restart-budget exhaustion with fail-closed recovery, atomic convergence after in-flight commit loss, and explicit stale-primary fencing plus rewind into a read-only follower. The evidence remains disposable and excludes automatic election or failover, automatic fencing, production process supervision, distributed partitions, production load-balancer behavior, and production certification. The product remains unselected until authenticated multi-node, failure, lifecycle, backup/restore, upgrade, replacement, and complete platform/lakehouse object evidence passes.

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

