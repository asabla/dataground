# Workspace change export

`internal/workspace` can capture a bounded final workspace tree and compare it with an exact verified source archive. `cmd/dataground-workspace-export` exposes that operation as an internal sandbox helper. This is a development prerequisite for specification section 9.7. The worker does not call it, the published source candidate does not install it, and no current invocation gains a change-export or teardown guarantee from this command.

## Capture boundary

The trusted caller must stop all workload writers and retain that quiescence fence until export completes. Two matching full reads detect observed drift; they do not prove that writers stopped or provide an atomic filesystem snapshot. The helper never stops processes, grants authority, changes workspace files, or permits teardown.

The pinned OpenShell [gateway contract](https://github.com/NVIDIA/OpenShell/blob/d556748771c41cbbd4e4dd7cd9030c798afe2b7d/proto/openshell.proto) exposes no stop, pause or quiesce operation. Its [process handle](https://github.com/NVIDIA/OpenShell/blob/d556748771c41cbbd4e4dd7cd9030c798afe2b7d/crates/openshell-supervisor-process/src/process.rs#L811-L848) signals the direct child, and its [SSH output drain](https://github.com/NVIDIA/OpenShell/blob/d556748771c41cbbd4e4dd7cd9030c798afe2b7d/crates/openshell-supervisor-process/src/ssh.rs#L906-L915) explicitly accounts for background descendants. A completed native command or closed SSH connection therefore does not establish the required fence for arbitrary workloads.

Capture requires an image-owned parent and workspace owned by the effective user. Directories and regular files must not be group- or world-writable and must have no special permission bits. Regular files must have one hard link. Symbolic links must resolve within the captured tree without cycles or missing targets. Files, directories and contained links are supported; sockets, FIFOs, devices, `.git` paths, cross-device entries, oversized trees and unsafe paths fail closed. File opens reject a final symbolic link and use nonblocking mode, with identity and metadata checks before and after each read. Root-relative access prevents a path from escaping the workspace. These checks supplement the required caller-owned quiescence fence.

The source limits also apply to the result: 4,096 entries, 24 MiB of regular-file content and a 32 MiB canonical tar archive. Capture preserves file bytes, directory presence, symbolic-link text and executable intent. It normalizes permissions to private source modes and omits timestamps, ownership and extended attributes. It does not follow links to read file content. Errors and receipts contain no paths, source bytes or operating-system diagnostics.

## Change artifact

The private artifact uses `dataground.workspace-changes/v1`. It binds the exact base archive digest and a deterministic result archive digest. Changes are sorted by path. Each existing path has a before-node digest; replacement nodes contain a kind and, when applicable, literal binary content encoded as JSON base64, executable intent or a contained link target. A missing before digest denotes an addition, and a missing replacement denotes deletion. Renames are represented as deletion and addition. An empty change list is valid. Parent directories are explicit, so a patch cannot silently restore a directory that it deletes.

The encoding is canonical Go struct JSON, without whitespace or a trailing newline, and is bounded to 64 MiB. The reader checks the artifact digest, exact base, ordered unique paths, before-node digests, normalized node forms, tree invariants and exact result digest. It rejects unknown or duplicate fields, redundant changes, ambiguous encodings and trailing data. `ApplyChanges` returns a new verified snapshot; it does not patch a live filesystem. The receipt contains only schema version, artifact digest and byte count, base and result archive digests, change count, result entry count and result source-byte count. It is metadata, not authorization or proof of durable storage. Artifact bytes are sensitive and must enter governed storage rather than events or logs.

## Helper command

The command reads the original source tar from standard input and writes the complete private change artifact to standard output. Only `/sandbox/workspace` (the default) and `/workspace` are accepted destinations; the parent ownership requirements still apply. For example, inside an explicitly prepared, quiesced sandbox:

```sh
dataground-workspace-export \
  --archive-digest sha256:<exact-base-archive-digest> \
  --destination /sandbox/workspace < base.tar > changes.json
```

Duplicate flags and positional arguments are rejected. The helper validates the base before capture and bounds its lifetime to one minute. Cancellation closes blocked input and output. A failure has nonzero exit status and the fixed diagnostic `WORKSPACE_CHANGE_EXPORT_FAILED`. Output may be partial after an I/O failure or cancellation. A consumer must require successful exit, complete stream closure and verified artifact bytes before accepting export. A retry requires the same base and a still-quiesced tree; this helper retains no delivery state.

The separate [native process owner](native-process-owner.md) supplies an internal terminal descendant-cleanup primitive for a dedicated host. Its opt-in owned runtime transport can confirm terminal cleanup before this export, as exercised by the strict gateway fixture. A separate internal controller can retain that exact connection's cleanup intent and confirmation under the original runtime claim. The worker is not connected to that composition; callers still have to establish and retain the complete writer fence. Cleanup records do not grant export or teardown authority.

## Internal OpenShell transport

`Provider.ExportWorkspaceChanges` uses the existing gateway-authorized SSH tunnel for an exact isolation-domain execution reference. It sends only the verified base digest in the fixed helper command and streams the exact original tar on standard input. No private file path, file content, caller-selected command or credential enters the command arguments. Cross-domain and non-ready targets fail before the proxy starts.

The caller must own current export authority, retain the workload quiescence fence and select an accepted image that contains the helper. The transport does not establish any of these conditions. Its one-minute bound covers concurrent input, output and diagnostic streams. It requires complete input delivery, at most 64 MiB of output, no remote diagnostic, successful exit and confirmed transport closure. It then independently checks the canonical change artifact against the retained base and returns privately owned verified bytes. Invalid or incomplete output is cleared and produces the fixed internal error `workspace change export could not be confirmed`.

A failed or lost acknowledgement leaves export unconfirmed and must retain the workspace. A repeat read can recover while the same tree remains quiesced and current authority still permits export. This provides no durable delivery receipt or teardown permission. The worker and published image remain unchanged.

## Internal export authorization

`AuthorizeInvocationWorkspaceChangeExport` is a separate database-authorized development boundary. Policy version 7 adds the Cedar `export-changes` action on an exact isolation-scoped `WorkspaceSource`. Wildcard policies in versions 1 through 6 cannot grant this action. Version 7 keeps the earlier source, publication, question and approval contracts and uses its own bundle digest domain and canonical schema. It is selected explicitly when installing a new revision policy:

```sh
go run ./cmd/dataground-policy-install \
  --workspace-export-capable \
  --isolation-domain iso_<id> --service svc_<id> --revision rev_<id> \
  --policy-set reviewed-export --policy-file /absolute/reviewed-policy.cedar \
  --entity-file /absolute/reviewed-entities.json \
  --actor operator --reason 'reviewed workspace export authority' \
  --correlation-id cor_<id>
```

The command retains the existing owner-only file checks and immutable per-revision installation. The export flag is mutually exclusive with the other contract flags. Existing source-worker and source-publication profiles still require version 6; they do not accept this new contract. Installing version 7 does not enable worker export.

Authorization requires the exact confirmed schema 73 cleanup record, original completed source preparation and runtime attempt, current immutable execution plan, ready execution instance, and a current running version 2 invocation claim with an unexpired lease and deadline. A replacement claim can export the stopped workspace, but must retain the original runtime token and owner binding. Historical cleanup lookup alone grants no authority. The database trusts the internal cleanup observer; it does not inspect Linux processes or exclude other writers.

The `capture` phase has no result receipt. The `retain` phase requires an exact valid change receipt for the same source base. Cedar receives bounded scope, actor, correlation, execution, effect, plan, original-owner binding, original and current fencing tokens, phase and receipt metadata. It receives no paths, source bytes, native nonce, command strings or credentials. Source reads remain subject to their separate current grant and Cedar authority.

Schema 74 records each completed policy outcome in the append-only `workspace_change_export_decisions` stream, with the exact request, current claim, deadline and effective policy provenance. Missing or withdrawn policy records a denial without policy provenance. Entity activation and withdrawal serialize with evaluation under the existing exact-scope policy lock. Database failure returns no permit; expiry during evaluation records a denial even if Cedar allowed the request. The caller receives a short-lived permit only after a successful decision commit. Invalid claims, malformed requests and unconfirmed cleanup fail before policy evaluation. A downgrade refuses to discard these decisions or version 7 policy evidence.

This method performs no source access, capture, artifact write or teardown. A caller must repeat authorization immediately before capture and each retention effect, retain the complete writer fence, and verify the accepted image. The internal controller below composes these checks; direct transport and retention-catalog callers still own them.

## Internal durable retention

`workspace.RetainChanges` accepts verified private change bytes and an exact `dataground.invocation-workspace-change-export/v1` binding. Its caller must already hold export authority. This internal database-authorized boundary does not resolve Cedar export policy, read a live workspace, establish quiescence or authorize teardown. It is not connected to the worker.

Schema 72 retains append-only intent and completion records. Intent binds one isolation domain, invocation, operation, effect, execution instance, original prepared source, execution-plan digest and change receipt. Preparation requires a current running version 2 invocation claim, an unexpired lease and deadline, exact actor and correlation, a ready execution instance, the original runtime attempt and completed source materialization. It also requires the published version 2 execution plan to retain that same source and digest. Metadata contains no private file paths or change content.

Intent and its audit and outbox records commit before object access. The fixed sensitive artifact uses the existing invocation artifact store and a deterministic operation-scoped identifier. Retention first observes the destination. An existing exact object needs no new write; a missing object permits a conditional immutable write only after a fresh claim check. Unknown or conflicting observations fail closed. Every write outcome requires independent bounded read-back and digest verification. Completion rechecks the current claim and atomically publishes the governed artifact descriptor, object binding, completion, audit and outbox.

A lost object acknowledgement or failed completion leaves the intent pending. A replacement worker with a current claim can recover the same object without another write. It cannot substitute another export, change the original runtime-attempt owner or token, or start another native turn through this boundary. Completed exact replay is read-only after lease expiry and still verifies the retained object and available artifact descriptor. A missing, corrupted or deleted artifact cannot be recreated through completed replay. A schema downgrade refuses retained export evidence.

## Internal export controller

`reconcile.InvocationWorkspaceExporter` composes the current claim, exact confirmed cleanup, source access, OpenShell change transport and durable artifact retention. Its immutable configuration pins one isolation domain, service revision, source record, execution-plan digest and source-grant generation. Its caller supplies the exact execution reference and a check for current runtime authority, accepted helper image and continued exclusion of other writers. The worker does not call this controller yet, and the current accepted source-image contract does not include the export or native-owner helpers.

The controller requires the original confirmed cleanup record before reading the base or calling capture. `Repository.ReadExportWorkspaceSource` can read that original source under a current replacement lease without authorizing another native turn. It reuses current source-grant and Cedar checks at both read boundaries, requires policy version 7, and bounds delivery by the earlier grant, lease and operation deadline. Schema 75 records this purpose as `dataground.invocation-source-read/v5`, with an isolation-scoped foreign key to the exact cleanup completion. Older runtime-read and pre-start preparation contracts retain their original claim semantics. A downgrade refuses to discard export-source decisions.

For a new export, the controller obtains capture authority, reads and verifies the base, obtains fresh capture authority and calls the existing bounded transport. It requires the complete verified change receipt before retention. Every artifact read, write, catalog preparation and completion obtains fresh receipt-bound retention authority and repeats the caller's runtime/image/writer check. Child contexts cannot extend an earlier authorization or source-read window. An authority failure ends that attempt; a finalizer cannot bypass it through read-back after a failed write. Success requires one final current decision, including when completion already existed.

Recovery first reads the exact retained intent. An existing object must pass complete bounded read-back and independent change verification against the original base. A replacement worker can then complete the retained export without another capture or object write. A pending intent with no object permits a fresh authorized capture only if its receipt matches the immutable intent. A corrupt object, a missing completed object, a changed receipt or unavailable artifact descriptor fails closed and cannot cause recreation. This controller never starts a native turn, removes workspace data, changes invocation lifecycle state or grants teardown authority.

## Verification and limits

Package and command tests cover binary round trips, additions and deletions, empty directories, type replacement, executable normalization, contained links, exact repeated capture, base and result substitution, malformed encodings, unsafe node types, hard links, escaping or dangling links, limits, cancellation and unchanged live workspace contents. The command tests use the real capture and apply boundary with temporary trees; the Linux test binaries also run on Linux ARM64. SSH transport tests also cover target substitution, source and result substitution, output and diagnostic limits, failed exit, lost acknowledgement and cancellation after partial output. Existing source-transfer tests protect the shared stream-exchange behavior.

An opt-in Docker integration builds both helpers into a disposable layer over an exact image. It uses a controlled SSH peer to export real Linux binary changes, recover a lost acknowledgement, repeat the same artifact, reject an escaping link and verify unchanged workspace contents. The test owns the only writer. It does not establish live gateway authorization or a production quiescence mechanism. For example:

```sh
DATAGROUND_TEST_WORKSPACE_CHANGE_BASE_IMAGE=sha256:<local-image-digest> \
  go test ./internal/execution/openshell -run '^TestWorkspaceChangeDockerExport$' -count=1
```

Durable retention tests exercise real PostgreSQL claims and the artifact object-store boundary, including expiry before a write and after a successful write, lost acknowledgements, audit rollback, replacement-worker recovery, concurrent historical replay, scope substitution, append-only records, deletion and protected downgrade. The existing live source-grant fixture also selects this retention case against the pinned S3 candidate.

PostgreSQL authorization tests use real source bytes, in-memory object ports and trusted cleanup observations. They cover pending cleanup, current and replacement claims, exact scope and owner binding, both phases, malformed receipts, old-policy denial, entity refresh, withdrawal, decision-storage failure, expiry during evaluation, append-only decisions and protected downgrade. The composed case also covers source revocation between admission and delivery, failed delivery audit, lost object acknowledgement, failed completion audit, replacement-worker recovery without capture or rewrite, corrupt or missing completed objects, and protected source-read downgrade. Controller tests cover each authority denial, image/writer-check failure, scope and receipt substitution, cancellation and deadline narrowing. These tests do not establish live workload quiescence, real gateway capture through this controller or accepted publication.

CI runs the Docker integration with the same pinned disposable base as the source-transfer test. The image layer is removed after the test and is not a published or accepted candidate.

The strict supervisor workflow also builds an unpublished export-helper layer over its source-capable runtime fixture. With `DATAGROUND_TEST_WORKSPACE_CHANGE_REQUIRED=true`, the provider-bound native test requires source transfer and a strict supervisor selection, performs bounded native binary writes, additions and deletion, then exports through the real gateway. It verifies exact canonical bytes, reconstructs the final tree, repeats export, rejects a foreign isolation domain and rejects an escaping link before recovering the same original export. All writers belong to the test and finish before capture. This does not prove that arbitrary background writers have stopped. The layer is used for synthetic credential and compatibility checks only; the workflow publishes only its separately tested supervisor image when explicitly requested. The published runtime and its acceptance contract remain unchanged.

The version 2 governed cancellation driver preserves an existing source-selected execution instead of deleting it or treating a prior termination observation as safe completion. It records `WORKSPACE_RETENTION_REQUIRED` through the ordinary retry boundary. The immutable source selection keeps this check in force after policy withdrawal and worker replacement. This is a preservation check, not an export or deletion permit; even a completed change export cannot bypass it. See [the source-worker limits](governed-worker.md) for deadline behavior.

This is a final difference, not an ordered file-write journal. A complete writer fence, accepted image installation, worker integration of the export controller, export before teardown, `termination_blocked` recovery, repository/base-revision metadata, ordered journals and checkpoints remain separate work. No production or release acceptance is established here.
