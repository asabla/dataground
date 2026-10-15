# Workspace source access

Source installation and source use are separate boundaries. The [source catalog](workspace-source-catalog.md) records immutable storage ownership. An internal `dataground.workspace-source-grant/v1` grant now permits reads of one exact ready source for one published service revision. This implements the scoped, short-lived source-grant prerequisite in ADR-005. The command remains database-authorized development tooling; it is not public policy administration or worker provisioning.

## Grant and decision contracts

A grant binds the isolation domain, workspace, source identity, archive digest, storage-profile digest, and exact service revision. An activation must be current, use UTC timestamps at microsecond precision, and last no more than one hour. There is no wildcard or latest-revision selection. The first event is generation 1 activation. Each later activation or revocation uses the next generation. Revocation has no validity window and remains possible after revision retirement. An exact historical retry is read-only, including after expiry or revocation; it does not reactivate the source. Changed attribution, reason digest, scope, or validity conflicts with the retained event.

Schema migration 66 adds append-only grant events and source-use decisions. Database triggers serialize grant changes with authorization, including direct administrative SQL, and reject generation gaps, invalid transitions, wrong source digests, missing ready sources, and activation for unpublished revisions. A grant change commits with its audit and outbox record. Correlation uniqueness is isolation-domain scoped. Downgrade cannot remove retained grant or decision evidence.

The internal reader requires an independently pinned source record, revision, and grant generation. It first commits a `read-admission` allow or deny decision. After an allow, it checks the configured storage-profile digest, reads the exact bounded object, verifies its archive digest and normalized counts, and rereads the immutable catalog binding. It then commits a separate `read-delivery` decision for the same generation and expiry before returning a private validated snapshot. The read context expires no later than the grant. Rotation, revocation, expiry, revision retirement, catalog drift, storage substitution, invalid archive bytes, or failed decision persistence prevents delivery. The reader has no storage-write path.

Use decisions contain platform scope and operation identities, requested and observed grant generations, archive and storage digests, phase, outcome, actor, correlation, and timestamps. They contain no source paths or bytes. Invalid command input and failures before an authorization evaluation do not produce a use decision. A lost grant commit acknowledgement requires the exact change to be retried. A lost use-decision acknowledgement releases no content; a new verification may record another decision.

These grants authorize a point-in-time source read. The trusted caller must separately validate the initiating actor, operation ownership, Cedar authority, and any provisioning lease. The reader does not validate those bindings against an invocation operation. Expiry or revocation prevents subsequent reads and prevents delivery when detected during a read; it does not erase content already delivered. A later sandbox creation or source transfer needs a fresh effect authorization and its own recovery boundary.

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

Public Cedar-authorized source operations, authenticated source-use subjects, invocation claim and lease binding, verified sandbox transfer, image integration, repository provenance and credentials, checkpoints, export, retention, deletion, and production certification remain required. The governed worker does not yet consume these source grants or snapshots.
