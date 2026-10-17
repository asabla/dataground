# Workspace source access

Source installation and source use are separate boundaries. The [source catalog](workspace-source-catalog.md) records immutable storage ownership. An internal `dataground.workspace-source-grant/v1` grant now permits reads of one exact ready source for one published service revision. This implements the scoped, short-lived source-grant prerequisite in ADR-005. The command remains database-authorized development tooling; it is not public policy administration or worker provisioning.

## Grant and decision contracts

A grant binds the isolation domain, workspace, source identity, archive digest, storage-profile digest, and exact service revision. An activation must be current, use UTC timestamps at microsecond precision, and last no more than one hour. There is no wildcard or latest-revision selection. The first event is generation 1 activation. Each later activation or revocation uses the next generation. Revocation has no validity window and remains possible after revision retirement. An exact historical retry is read-only, including after expiry or revocation; it does not reactivate the source. Changed attribution, reason digest, scope, or validity conflicts with the retained event.

Schema migration 66 adds append-only grant events and source-use decisions. Database triggers serialize grant changes with authorization, including direct administrative SQL, and reject generation gaps, invalid transitions, wrong source digests, missing ready sources, and activation for unpublished revisions. A grant change commits with its audit and outbox record. Correlation uniqueness is isolation-domain scoped. Downgrade cannot remove retained grant or decision evidence.

The internal reader requires an independently pinned source record, revision, and grant generation. It first commits a `read-admission` allow or deny decision. After an allow, it checks the configured storage-profile digest, reads the exact bounded object, verifies its archive digest and normalized counts, and rereads the immutable catalog binding. It then commits a separate `read-delivery` decision for the same generation and expiry before returning a private validated snapshot. The read context expires no later than the grant. Rotation, revocation, expiry, revision retirement, catalog drift, storage substitution, invalid archive bytes, or failed decision persistence prevents delivery. The reader has no storage-write path.

Use decisions contain platform scope and operation identities, requested and observed grant generations, archive and storage digests, phase, outcome, actor, correlation, and timestamps. They contain no source paths or bytes. Invalid command input and failures before an authorization evaluation do not produce a use decision. A lost grant commit acknowledgement requires the exact change to be retried. A lost use-decision acknowledgement releases no content; a new verification may record another decision.

These grants authorize a point-in-time source read. The trusted caller must separately validate the initiating actor, operation ownership, Cedar authority, and any provisioning lease. The administrative reader does not validate those bindings against an invocation operation. The separate claim-bound reader below validates operation ownership and the active runtime attempt. Expiry or revocation prevents subsequent reads and prevents delivery when detected during a read; it does not erase content already delivered. A later sandbox creation or source transfer needs a fresh effect authorization and its own recovery boundary.

## Revision source selection

`dataground.execution-plan/v2` requires one `workspaceSource` containing the complete immutable source record. Its domain must match the revision. The canonical plan digest covers the workspace and source identities, archive and storage-profile digests, byte counts, and entry count. The grant generation is not part of this permanent selection: each read must independently pin a current short-lived grant. Version 1 remains source-free and preserves its canonical bytes and digest.

The existing `dataground-execution-plan-install` command accepts this normalized version 2 contract with the same owner-only file, explicit scope, and reviewed digest requirements described in [governed worker setup](governed-worker.md#install-a-reviewed-execution-plan). For example, set `schemaVersion` to `dataground.execution-plan/v2` and set `workspaceSource` to the exact record returned by source installation before canonical serialization. Installation requires a draft revision and an exact completed source in PostgreSQL. Missing, incomplete, substituted, or cross-domain records fail closed. The plan and its audit commit together. Exact replay remains read-only after publication; new or changed selection requires another draft revision.

Migration 68 enforces source-plan identity against the complete catalog record and rejects updates or deletion of source-bearing plans, including cascading revision deletion. Downgrade cannot remove those bindings. Deletion therefore needs a future governed retention boundary. PostgreSQL plan reads now recompute and compare the canonical digest for both versions. The process-local conformance store validates and copies the record but does not establish catalog existence or publication state.

The default admission and worker profiles accept only source-free version 1 plans. The explicit [source worker profile](governed-worker.md#use-an-accepted-source-plan) admits one exact version 2 plan with version 3 runtime acceptance and requires claim-bound source preparation before runtime startup. Atomic operator publication and the explicit [version 5 authorized publication path](governed-publication-queue.md#publish-a-source-backed-revision) can accept that plan. The version 3 operator queue remains source-free. A plan alone establishes neither source authority nor runtime certification.

## Invocation claim binding

`Repository.ReadClaimedWorkspaceSource` is an internal read path for one reserved invocation runtime attempt. It takes an exact source record and grant generation, an operation claim, the prepared `run-invocation` effect, a read-only object port, and the required pure `SourceCedarEvaluator`. It derives the revision, initiating actor, and correlation from the durable invocation target and resolves that revision's version 2 execution plan in the same transaction. The supplied source record is an expected pin and must equal the complete stored selection; it cannot select a different source. A missing, source-free, or invalid plan prevents the read. Caller-carried attribution and deadline hints cannot replace those values. A repair operation retains its stored effect attribution.

Before storage access and again before delivery, the grant transaction locks the invocation and exact operation, verifies the effect identity and request digest, and requires a reserved runtime attempt with the same owner and fencing token. The operation must still be running under the supported state-machine version, its admission effect must have succeeded, and both its lease and deadline must be current. Cancellation, replacement, completion, wrong scope, or a changed effect prevents delivery. Operation lock contention fails closed. Grant rotation and revocation use the same checks as administrative reads.

The read context ends at the earliest of the caller deadline, the initially observed database lease expiry, the operation deadline, and the grant expiry. A later renewal cannot extend that read. The audit insert rechecks database time after lock waits. No transaction or operation lock remains open during object I/O.

Migration 67 introduced the optional `dataground.invocation-source-read/v1` binding. Migration 68 added planned reads as `dataground.invocation-source-read/v2`. Migration 69 preserves both historical forms and records new Cedar-authorized planned reads as `dataground.invocation-source-read/v3`. It records the invocation, effect, owner, fencing token, earlier database lease/deadline expiry, plan digest, active policy contract, policy-set identifier, effective policy digest, and Cedar outcome with each completed grant evaluation. All binding fields must be present together. Invalid or stale claims fail before a grant evaluation and do not create a source-use decision. Migration downgrade cannot remove retained claim evidence. Administrative decisions retain their existing grant-only contract and have no claim binding.

This method is a read prerequisite, not a provisioning effect. The reader makes the source-use decision for this read. It does not install a sandbox tree or provide a reusable transfer permit; a future transfer needs its own current effect authorization and recovery record. The governed worker does not call this method yet.

## Source reads after confirmed cleanup

The separate `Repository.ReadExportWorkspaceSource` boundary supports original and replacement invocation claims after exact confirmed workspace cleanup. It uses the immutable source plan and original runtime preparation, requires current version 7 Cedar source authority, and preserves both current source-grant decisions. It returns the verified base and the earlier read/lease/deadline limit. Schema 75 binds its version 5 source-use decisions to the isolation-scoped cleanup completion. It cannot adopt pending cleanup or restart the original runtime. See the [export controller](workspace-change-export.md#internal-export-controller) for composition with capture and retention authority. The existing `ReadClaimedWorkspaceSource` method still requires the original active runtime attempt.

## Source-read Cedar policy

Install `dataground.invocation-authorization-policy/v6` with the existing policy installer and its mutually exclusive `-source-capable` flag. This contract adds `read-source` to the version 5 actions and keeps the existing immutable policy, entity-generation, activation, and withdrawal boundaries. An older wildcard policy cannot grant the new action. The explicit source worker requires version 6; other worker profiles and public publication retain their existing policy versions.

For example, an explicitly installed source policy can restrict the actor role, exact source, revision, and both read phases:

```cedar
permit (
  principal in DataGround::Role::"source-reader",
  action == DataGround::Action::"read-source",
  resource == DataGround::WorkspaceSource::"iso_00000000000000000001:wsp_00000000000000000001:src_00000000000000000001"
) when {
  context.revisionID == "rev_00000000000000000001" &&
  (context.phase == "read-admission" || context.phase == "read-delivery")
};
```

The resource identity includes the isolation domain, workspace, and source. The context carries the stored invocation and service identities, source and storage digests, plan digest, effect, fencing token, requested grant generation, phase, and correlation. It contains no source paths, archive bytes, credentials, or native endpoints. Actor and role membership come from the stored invocation attribution and current canonical entity generation.

Each read phase holds the invocation claim, policy scope, and grant locks while evaluating Cedar and recording the result. The pure evaluator performs no database or storage calls. Direct SQL entity activation and policy withdrawal take the same policy-scope lock. Refresh, withdrawal, cancellation, expiry, or grant changes between phases therefore prevent delivery when the new state denies it. No locks remain open during object I/O.

The effective policy digest and separate Cedar outcome commit atomically with the overall grant decision. A grant can deny a Cedar-allowed read; it cannot override a Cedar denial or unavailable result. Missing policy resolution and invalid claims fail before evaluation. Failed audit persistence or cancellation releases no content. Downgrade cannot discard source-policy evidence. The administrative verification command below retains its database-authorized, grant-only behavior.

## Development command

`cmd/dataground-workspace-source-access` provides `activate`, `revoke`, and `verify`. All subcommands require an existing ready source, the current database schema, exact reviewed source and storage digests, an explicit revision and generation, actor, and correlation. The administrative database credential is the command's trust boundary. Each flag is accepted once; unrelated flags and extra arguments are rejected.

For example, prepare the common reviewed arguments in a shell array:

```shell
source_args=(
  --isolation-domain iso_00000000000000000001
  --workspace wsp_00000000000000000001
  --source src_00000000000000000001
  --archive-digest 'sha256:<reviewed-archive-digest>'
  --storage-profile-digest 'sha256:<reviewed-storage-profile-digest>'
  --revision rev_00000000000000000001
  --actor reviewed-operator
)
```

With `DATAGROUND_DATABASE_URL` set to the administrative database, activate a current grant with independently reviewed times:

```shell
go run ./cmd/dataground-workspace-source-access activate "${source_args[@]}" \
  --generation 1 \
  --activated-at '<current-UTC-activation-time>' \
  --expires-at '<UTC-expiry-within-one-hour>' \
  --reason 'reviewed source read for this revision' \
  --correlation-id cor_00000000000000000001
```

Verify the actual retained source under that exact grant:

```shell
go run ./cmd/dataground-workspace-source-access verify "${source_args[@]}" \
  --generation 1 \
  --operation-id op_00000000000000000001 \
  --storage-endpoint http://127.0.0.1:8333 \
  --storage-bucket dataground-conformance \
  --correlation-id cor_00000000000000000002
```

Verification accepts only a literal loopback HTTP origin and path-style storage, with no proxy or redirects. It uses bounded requests and a two-minute operation context, further restricted by grant expiry. It emits only `dataground.workspace-source-verification/v1` receipt metadata: the source record, revision, generation, and materialization inspection counts. It writes no workspace files and emits no archive content. Activation and revocation produce no standard output. Every command failure emits only `WORKSPACE_SOURCE_ACCESS_FAILED` and exits nonzero; failure does not imply that a grant transaction rolled back.

Revoke the source with the next generation and a new stable correlation:

```shell
go run ./cmd/dataground-workspace-source-access revoke "${source_args[@]}" \
  --generation 2 \
  --reason 'withdraw source read authority' \
  --correlation-id cor_00000000000000000003
```

## Evidence and remaining integration

Tests cover bounded validity, canonical input, concurrent activation replay, changed grants, cross-domain and workspace rejection, exact generation and digest binding, missing or expired grants, direct SQL revocation serialization, revocation and revision retirement during reads, grant and use-audit failure, immutable evidence, downgrade refusal, malformed content, storage substitution, and read deadlines. CI also composes the grant/read path with the pinned disposable S3 candidate and PostgreSQL. Successful verification proves that read only; it is not a lease or release certificate.

Tests cover source-policy installation through the operator command, old-wildcard rejection, context restrictions, membership removal between read phases, denied and unavailable audit outcomes, typed-nil evaluator rejection, direct SQL withdrawal serialization, and version 6 entity refresh. They also cover source-plan installation through the operator command, complete-record matching, incomplete-source rejection, concurrent replay, atomic audit failure, replay after publication, immutable selection, admission rejection, digest corruption, and downgrade refusal. They also exercise the claim-bound read with durable invocation acceptance, admission, and a reserved runtime attempt, including forged attribution and timestamps, stale claims, cancellation during storage access, attempt completion, lease renewal, expiry, failed delivery audit, and claim-evidence downgrade refusal.

An [internal sandbox transfer](workspace-source-materialization.md#internal-sandbox-transfer) is tested separately with the real Linux helper and a controlled SSH peer. A [durable preparation gate](workspace-source-materialization.md#durable-preparation-and-runtime-gate) now binds pre-runtime reads and completion to the current worker lease. An [internal provisioning controller](workspace-source-materialization.md#internal-provisioning-controller) connects these boundaries in an explicitly configured runtime driver. The explicit source worker now consumes these preparation, source grant, and snapshot boundaries with independently accepted image and gateway pins. Public Cedar-authorized source operations, live source-worker acceptance, repository provenance and credentials, checkpoints, export of changes, retention, deletion, and production certification remain required.
