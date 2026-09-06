# Governed development worker

`dataground-worker` keeps the deterministic PostgreSQL reference driver as its default. An explicit `governed-development` mode composes the complete version 2 invocation lifecycle through the existing OpenShell admission, Codex runtime, durable interactive approval mediation, cancellation, Cedar authorization, decision-audit, provider-profile mediation, and artifact-finalization boundaries. Publication effects and version 1 invocation effects continue to use the deterministic reference driver.

This mode is intentionally limited to one development isolation domain, the pinned OpenShell `0.0.86` Docker profile at `http://127.0.0.1:8080`, provider profile `codex`, and anonymous path-style S3 on a loopback plaintext endpoint. Admission additionally requires runtime profile and gateway capability `codex.app-server/v1` plus the development profile's exact digest-pinned sandbox image; profile drift fails before provider placement. The mode does not add workload authentication, provider provisioning, production policy distribution, Rosetta materialization, audit export or retention, backend certification, or public execution guarantees. Governed startup now requires one operator-supplied, exact accepted runtime-certification manifest and its reviewed conformance pair. No live record or certification manifest is checked into the repository, and this activation is not production certification.

The PostgreSQL schema must be current. The exact service revision must already have an immutable execution plan, enforcement-bundle catalog record and object, installed approval-capable version 3 invocation Cedar policy, and a current `agent-inference` grant for every provider profile in that plan. The configured OpenShell gateway and `codex` provider must already exist. The repository does not yet expose a supported public workflow for provisioning those execution inputs; missing or unavailable inputs fail closed through the durable retry or terminal-failure contracts.

The governed process also owns question and approval expiry for its configured isolation domain. Before startup completes it commits bounded question and approval expiry batches, then polls independently of the potentially blocking runtime reconciler every 250 ms. Each sweep shares a two-second database deadline across sequential batches of at most 100 due questions and 100 due approvals, using PostgreSQL time and row-lock skipping. Expiry atomically records the terminal state, audit and outbox evidence under the respective actor `dataground-question-expiry` or `dataground-approval-expiry` and a fresh batch correlation; delivery already reserved becomes `delivery_unknown` and is never retried. Lost worker leases do not prevent expiry, and concurrent governed processes safely divide due rows through the database locks.

An expiry failure, a batch that exceeds its deadline, stale progress, or owner shutdown withholds governed readiness at the existing execution boundaries. A later successful batch restores that part of readiness without bypassing runtime certification. Expiry continues while certification readiness is lost, and shutdown cancels and joins the owner before closing other worker resources or the database pool. Reference mode starts no expiry owner. This supplies lifecycle maintenance for existing question and expiring approval records. The internal driver also has the question mediation described in [OpenShell development](openshell-local.md), but this command still builds question-disabled requests; question-mode activation and live question certification remain separate work. A running governed process is still required for this development maintenance path.

The governed outbox dispatcher claims only its configured isolation domain and rejects a foreign claim before publishing, acknowledgement or retry. The shared repository uses PostgreSQL time for eligibility and lease expiry, rechecks pending status after a concurrent row-lock wait, and derives retry exhaustion from the persisted attempt count. Reference workers retain the existing cross-domain development dispatch. The current development publisher acknowledges outbox rows locally; external delivery and projection of interaction closure into the invocation journal remain separate work.

## Install a reviewed execution plan

An operator with administrative database access can install the plan through `go run ./cmd/dataground-execution-plan-install`. This command exposes the existing immutable plan-binding transaction; it does not create a revision, publish enforcement material, grant provider access, or certify a runtime. The revision must already exist in the exact isolation domain with the plan's runtime profile and required capabilities. Complete provisioning of the other inputs above remains required.

Prepare one owner-only regular file containing the normalized `dataground.execution-plan/v1` contract from `internal/execution/plan.go`. Normalize the plan with `execution.NormalizeExecutionPlan`, serialize the returned plan with `json.Marshal`, and append exactly one newline: fields use the declared Go struct order, provider profiles and required capabilities are sorted and deduplicated, and no unknown fields, duplicate properties, additional JSON values, or extra whitespace are accepted. The maximum file size is 64 KiB. The reviewed digest is the lowercase `sha256:` value returned by `execution.DigestExecutionPlan`, which hashes the normalized compact JSON **without** the trailing newline. Review the environment, image, enforcement-bundle, runtime-matrix and provider-profile references against their deployment-owned evidence before installation; a matching digest establishes exact input identity, not certification of those references.

```shell
DATAGROUND_DATABASE_URL='<administrative-postgresql-url>' \
go run ./cmd/dataground-execution-plan-install \
  -plan-file '<owner-only-reviewed-plan.json>' \
  -plan-digest 'sha256:<reviewed-normalized-plan-digest>' \
  -isolation-domain '<exact-isolation-domain-id>' \
  -revision '<exact-service-revision-id>' \
  -actor '<authorized-operator-id>' \
  -correlation-id '<stable-installation-correlation-id>'
```

The command validates file identity, canonical content, explicit scope, attribution and digest before database access, then requires the current schema and uses a bounded database operation. Installation atomically writes the immutable plan and its `execution-plan.bind` audit record. Repeating the same normalized plan succeeds without changing the original attribution or duplicating audit evidence; changing any plan field for an already bound revision fails. If the commit acknowledgement is lost, retry the exact installation to resolve the outcome. The command emits no plan contents or database diagnostics.

No migration is introduced by this command. Removing the command does not remove installed plans; the existing append-only binding and governed effect-time authorization remain authoritative. A replacement plan requires a new service revision. Missing policy, enforcement material, provider grants or accepted runtime certification continues to block governed execution.

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

An approval identifier learned from `interaction.approval.requested` can be refreshed with `GET /v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/approvals/{approvalId}`. The durable read matches the invocation path before disclosure and has its own audited API Cedar action, so reconnecting clients can observe the authoritative pending, resolved, delivering, or delivered version without depending on local event reconstruction. Pending approvals are resolved with `POST` to the same path. The JSON body contains `expectedVersion: 1` and a `decision` of `approve` or `deny`, and the request requires an `Idempotency-Key`. API Cedar authorizes the path-derived approval before the body is read. The exact revision-bound approval-capable invocation policy then evaluates the authenticated principal, normalized action, decision, and `entry` phase inside the durable idempotent command; its append-only decision record must commit before the result is released. The approval state, minimized resolution audit record, and sanitized `interaction.approval.resolved` event commit together. Migration 53 freezes the decision, deciding principal, correlation and resolution time through subsequent delivery transitions. An effective delivery decision can narrow an approval to denial, but cannot elevate a denial; the reserved effective decision is also immutable. Upgrade validates this bound against retained records, and downgrade cannot remove these protections while approvals remain. Exact command replay returns the original response without reevaluating a later policy generation. Delivery still performs the independent `effect` authorization immediately before reserving the native acknowledgement and may safely reduce an approved decision to deny. The waiting driver rechecks readiness before reading a resolution, after effect authorization, after reservation, and before committing delivery acknowledgement. The native write has a context deadline no later than the current claim lease or operation deadline; cancellation is checked before and after readiness verification. Readiness loss or an expired claim after reservation leaves delivery ambiguous and cannot trigger a second native write.

The public read and resolution responses contain only the platform approval identity, invocation identity, normalized requested action, state, version, decision, deciding principal, and timestamps. They do not expose the operation, effect, Codex request, provider profile, sandbox, or adapter-local approval identifier. These narrow development routes do not provide approval listing, assignment, delegation, callbacks, modified requests, session-wide grants, or a production controller lease.

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

New approvals use `dataground.invocation-approval/v2` publicly and `dataground.invocation-runtime-approval/v2` internally. Migration 54 retains legacy v1 records without inventing expiry timestamps. New requests expire at the earlier of the durable operation deadline and 15 minutes after creation; an internal caller may request an earlier deadline. PostgreSQL checks expiry again when resolving, reserving delivery and recording acknowledgement. Pending or resolved requests become `expired`; a reserved delivery becomes `delivery_unknown`, preserving the original decision and preventing another send. Terminal transitions commit their safe audit and outbox records together. A live worker can also close a request after cancellation or runtime failure; a lost worker lease cannot exercise that authority.

The public v2 representation includes `expiresAt` and, after closure, `closedAt` and `closeReason`. A new resolution of an expired or closed request returns HTTP 410 with `INVOCATION_APPROVAL_EXPIRED`; an existing command receipt retains its original idempotent response. The Workbench shows terminal expiry and uncertain delivery explicitly, stops offering decisions at the deadline, and checks the deadline again in the submission handler. The native delivery context is bounded by the approval, operation and current lease deadlines. Approval waiting shares the runtime event loop with normal event persistence, lease renewal and question mediation. The worker retains at most 256 independent adapter-local approval routes and polls one in round-robin order every 250 ms; a duplicate live handle or an exhausted bound fails closed. Native request clearance closes the corresponding durable record, and terminal draining closes queued requests without delivering them. Native interruption has a two-second acknowledgement deadline so an unresponsive runtime cannot hold terminal cleanup indefinitely. Approval listing remains separate work.

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
