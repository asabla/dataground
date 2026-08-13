# Governed development worker

`dataground-worker` keeps the deterministic PostgreSQL reference driver as its default. An explicit `governed-development` mode composes the complete version 2 invocation lifecycle through the existing OpenShell admission, Codex runtime, durable interactive approval mediation, cancellation, Cedar authorization, decision-audit, provider-profile mediation, and artifact-finalization boundaries. Publication effects and version 1 invocation effects continue to use the deterministic reference driver.

This mode is intentionally limited to one development isolation domain, the pinned OpenShell `0.0.86` Docker profile at `http://127.0.0.1:8080`, provider profile `codex`, and anonymous path-style S3 on a loopback plaintext endpoint. Admission additionally requires runtime profile and gateway capability `codex.app-server/v1` plus the development profile's exact digest-pinned sandbox image; profile drift fails before provider placement. The mode does not add workload authentication, provider provisioning, production policy distribution, Rosetta materialization, audit export or retention, backend certification, or public execution guarantees. Governed startup now requires one operator-supplied, exact accepted runtime-certification manifest and its reviewed conformance pair. No live record or certification manifest is checked into the repository, and this activation is not production certification.

The PostgreSQL schema must be current. The exact service revision must already have an immutable execution plan, enforcement-bundle catalog record and object, installed approval-capable version 3 invocation Cedar policy, and a current `agent-inference` grant for every provider profile in that plan. The configured OpenShell gateway and `codex` provider must already exist. The repository does not yet expose a supported public workflow for provisioning those execution inputs; missing or unavailable inputs fail closed through the durable retry or terminal-failure contracts.

## API dispatch

The durable API can explicitly opt into this worker target without changing the public invocation route. Set `DATAGROUND_GOVERNED_DISPATCH_CONFIG_FILE` to an absolute path containing an owner-controlled regular file such as:

```json
{
  "contract": "dataground.api-governed-dispatch/v1",
  "isolationDomainId": "iso_00000000000000000001",
  "serviceId": "svc_00000000000000000001",
  "revisionId": "rev_00000000000000000001",
  "runtimeProfile": "codex.app-server/v1"
}
```

The configured API must use the same database as the worker. Invocation acceptance resolves the requested alias and verifies its published revision, service, isolation domain, and runtime profile against this complete immutable target in the command transaction. Any mismatch returns `INVOCATION_DISPATCH_TARGET_MISMATCH` without committing an invocation, execution operation, outbox event, command audit, or idempotency result. Omitting the file retains the existing reference behavior, and process-local API mode rejects the option.

This API constraint only determines which durable operation the governed worker may claim. Reference workers lease only `reference/v1` operations, while the governed worker leases only its certified service revision, so the two modes cannot race to execute this target. API action authorization still runs before command admission, and the governed worker remains authoritative for exact-revision Cedar authorization, provider-profile grants, runtime certification, OpenShell admission, claim fencing, cancellation, and artifact finalization. The configuration does not publish or provision the target revision, expose OpenShell details, enable non-loopback traffic, or establish production certification.

Pending approvals are resolved with `POST /v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/approvals/{approvalId}`. The JSON body contains `expectedVersion: 1` and a `decision` of `approve` or `deny`, and the request requires an `Idempotency-Key`. API Cedar authorizes the path-derived approval before the body is read. The exact revision-bound approval-capable invocation policy then evaluates the authenticated principal, normalized action, decision, and `entry` phase inside the durable idempotent command; its append-only decision record must commit before the result is released. The approval state, minimized resolution audit record, and sanitized `interaction.approval.resolved` event commit together. Exact command replay returns the original response without reevaluating a later policy generation. Delivery still performs the independent `effect` authorization immediately before reserving the native acknowledgement and may safely reduce an approved decision to deny.

The public response contains only the platform approval identity, invocation identity, normalized requested action, state, version, decision, deciding principal, and timestamps. It does not expose the operation, effect, Codex request, provider profile, sandbox, or adapter-local approval identifier. This narrow development route does not provide approval listing, assignment, expiry, delegation, callbacks, modified requests, session-wide grants, or a production controller lease.

Select the mode with the complete environment below:

```sh
DATAGROUND_DATABASE_URL='postgres://dataground:dataground@127.0.0.1:5432/dataground?sslmode=disable' \
DATAGROUND_WORKER_MODE='governed-development' \
DATAGROUND_DEVELOPMENT_ISOLATION_DOMAIN_ID='iso_00000000000000000001' \
DATAGROUND_CERTIFIED_SERVICE_ID='svc_00000000000000000001' \
DATAGROUND_CERTIFIED_REVISION_ID='rev_00000000000000000001' \
DATAGROUND_RUNTIME_CERTIFICATION_MANIFEST='deploy/openshell/evidence/runtime-certification.json' \
DATAGROUND_RUNTIME_CONFORMANCE_EVIDENCE='deploy/openshell/evidence/openshell-runtime-conformance-v1.json' \
DATAGROUND_RUNTIME_CONFORMANCE_ACCEPTANCE='deploy/openshell/evidence/openshell-runtime-conformance-acceptance-v1.json' \
DATAGROUND_RUNTIME_CERTIFICATION_SHA256='<64 lower-case hex characters>' \
DATAGROUND_RUNTIME_CERTIFICATION_SOURCE_REVISION='<40 lower-case hex characters>' \
DATAGROUND_RUNTIME_CERTIFICATION_MINIMUM_GENERATION='1' \
DATAGROUND_OPENSHELL_GATEWAY_ID='gw_00000000000000000001' \
DATAGROUND_OPENSHELL_GATEWAY_ENDPOINT='http://127.0.0.1:8080' \
DATAGROUND_OPENSHELL_POLICY_WORKSPACE='/absolute/private/policy-workspace' \
DATAGROUND_OPENSHELL_EXPORT_WORKSPACE='/absolute/private/export-workspace' \
DATAGROUND_S3_ENDPOINT='http://127.0.0.1:8333' \
DATAGROUND_S3_BUCKET='dataground-development' \
DATAGROUND_S3_REQUEST_TIMEOUT='2m' \
DATAGROUND_INVOCATION_ARTIFACT_MAX_BYTES='16777216' \
go run ./cmd/dataground-worker
```

`DATAGROUND_OPENSHELL_BINARY` may select the installed CLI path; it defaults to `openshell`. Both network endpoints must be plain HTTP loopback origins without embedded credentials, paths, queries, or fragments. The three certification inputs must be distinct clean repository-relative files. `DATAGROUND_RUNTIME_CERTIFICATION_REJECTED_IDS` may contain a comma-separated set of withdrawn or consumed certification identifiers; an empty value is rejected, so omit it when the set is empty. The worker disables proxy inheritance for S3. The S3 request timeout must be between one second and ten minutes. The policy and export workspaces must be distinct non-root absolute paths; their workspace implementations additionally require owner-held mode-0700 directories and exclusive locks. The artifact limit must be between one byte and one GiB.

Startup requires the exact database schema, validates the complete mode configuration, verifies the existing certification manifest checker against the exact target and active generation/replay inputs, proves that the published database revision belongs to that service, acquires both private workspaces, composes every governed dependency, verifies the pinned OpenShell CLI version, and idempotently registers the exact gateway configuration before polling begins. The worker leases operations only for the configured isolation domain, service, and revision; it cannot advance attempts or state for unrelated scope. It does not turn dependency availability into a false startup guarantee: gateway and object-store outages retain their existing durable reconciliation behavior.

Admission, runtime, approval resolution, and cancellation share the same exact-revision PostgreSQL policy source and audited Cedar authorizer. A completed allow, deny, or evaluator-unavailable outcome is withheld until its append-only invocation-decision row commits. The runtime route remains claim-bound, consumes one durable attempt before starting Codex, renews the exact lease, persists normalized events, validates output, exports declared artifacts through the private workspace, and verifies immutable object storage before catalog binding. Interactive Codex prompts are assigned platform approval identifiers and atomically journaled with versioned pending state; resolution records the actual principal after entry authorization, and delivery reauthorizes immediately before reserving one native acknowledgement. A failed or restarted delivery after reservation remains ambiguous and is never repeated. Any missing phase or typed-nil dependency prevents worker startup; no version 2 phase can silently fall back to the reference driver. The exact verifier is rerun before polling, every governed effect, native runtime start, periodic runtime lease renewal, artifact export, and durable runtime completion. Missing, substituted, expired, withdrawn, replayed, stale-generation, or mismatched evidence removes readiness and stops new claims. A change during a native turn interrupts the turn and leaves its single-use attempt ambiguous rather than repeating it. Restoring readiness always reruns the verifier; no accepted result is cached across a worker restart.

## Provider credential mediation

The worker passes only the deployment-owned profile name to OpenShell. Provider keys, refresh tokens, cloud credentials, provider endpoints, and credential placement never enter the grant contract, execution plan, worker configuration, authorization decision, audit record, event, or harness request. OpenShell remains the credential holder and inference-routing boundary.

An authorized operator activates or revokes one exact sequential grant with the internal command. Activation times must be canonical UTC, already effective, no more than 24 hours apart, and still current when committed.

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-provider-credential-grant \
  -operation activate \
  -isolation-domain 'iso_00000000000000000001' \
  -revision 'rev_00000000000000000001' \
  -provider-profile 'codex' \
  -generation 1 \
  -activated-at '2026-08-09T10:00:00Z' \
  -expires-at '2026-08-09T18:00:00Z' \
  -actor 'operator-one' \
  -reason 'authorize reviewed OpenShell profile for this revision' \
  -correlation-id 'cor_00000000000000000001'
```

Use the next sequential generation with `-operation revoke` and omit both time flags for local revocation. Exact replay of the same generation is read-only; changed attribution or scope conflicts. Grant activation and revocation commit with the closed safe operator audit record. Every governed admission then records a separate append-only allow or deny for each requested profile at the admission boundary and again immediately before sandbox creation. Missing scope, a different domain or revision, an unregistered or substituted profile, expiry, and local revocation deny before the relevant provider effect.

This boundary does not acquire, refresh, rotate, distribute, inspect, or revoke the actual provider credential. It does not prove provider-side revocation, OpenShell configuration, model routing, direct-provider reachability denial, or production readiness. Those remain deployment and certification gates.
