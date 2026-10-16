# Workspace change export

`internal/workspace` can capture a bounded final workspace tree and compare it with an exact verified source archive. `cmd/dataground-workspace-export` exposes that operation as an internal sandbox helper. This is a development prerequisite for specification section 9.7. The worker does not call it, the published source candidate does not install it, and no current invocation gains a change-export or teardown guarantee from this command.

## Capture boundary

The trusted caller must stop all workload writers and retain that quiescence fence until export completes. Two matching full reads detect observed drift; they do not prove that writers stopped or provide an atomic filesystem snapshot. The helper never stops processes, grants authority, changes workspace files, or permits teardown.

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

## Internal OpenShell transport

`Provider.ExportWorkspaceChanges` uses the existing gateway-authorized SSH tunnel for an exact isolation-domain execution reference. It sends only the verified base digest in the fixed helper command and streams the exact original tar on standard input. No private file path, file content, caller-selected command or credential enters the command arguments. Cross-domain and non-ready targets fail before the proxy starts.

The caller must own current export authority, retain the workload quiescence fence and select an accepted image that contains the helper. The transport does not establish any of these conditions. Its one-minute bound covers concurrent input, output and diagnostic streams. It requires complete input delivery, at most 64 MiB of output, no remote diagnostic, successful exit and confirmed transport closure. It then independently checks the canonical change artifact against the retained base and returns privately owned verified bytes. Invalid or incomplete output is cleared and produces the fixed internal error `workspace change export could not be confirmed`.

A failed or lost acknowledgement leaves export unconfirmed and must retain the workspace. A repeat read can recover while the same tree remains quiesced and current authority still permits export. This provides no durable delivery receipt or teardown permission. The worker and published image remain unchanged.

## Internal durable retention

`workspace.RetainChanges` accepts verified private change bytes and an exact `dataground.invocation-workspace-change-export/v1` binding. Its caller must already hold export authority. This internal database-authorized boundary does not resolve Cedar export policy, read a live workspace, establish quiescence or authorize teardown. It is not connected to the worker.

Schema 72 retains append-only intent and completion records. Intent binds one isolation domain, invocation, operation, effect, execution instance, original prepared source, execution-plan digest and change receipt. Preparation requires a current running version 2 invocation claim, an unexpired lease and deadline, exact actor and correlation, a ready execution instance, the original runtime attempt and completed source materialization. It also requires the published version 2 execution plan to retain that same source and digest. Metadata contains no private file paths or change content.

Intent and its audit and outbox records commit before object access. The fixed sensitive artifact uses the existing invocation artifact store and a deterministic operation-scoped identifier. Retention first observes the destination. An existing exact object needs no new write; a missing object permits a conditional immutable write only after a fresh claim check. Unknown or conflicting observations fail closed. Every write outcome requires independent bounded read-back and digest verification. Completion rechecks the current claim and atomically publishes the governed artifact descriptor, object binding, completion, audit and outbox.

A lost object acknowledgement or failed completion leaves the intent pending. A replacement worker with a current claim can recover the same object without another write. It cannot substitute another export, change the original runtime-attempt owner or token, or start another native turn through this boundary. Completed exact replay is read-only after lease expiry and still verifies the retained object and available artifact descriptor. A missing, corrupted or deleted artifact cannot be recreated through completed replay. A schema downgrade refuses retained export evidence.

## Verification and limits

Package and command tests cover binary round trips, additions and deletions, empty directories, type replacement, executable normalization, contained links, exact repeated capture, base and result substitution, malformed encodings, unsafe node types, hard links, escaping or dangling links, limits, cancellation and unchanged live workspace contents. The command tests use the real capture and apply boundary with temporary trees; the Linux test binaries also run on Linux ARM64. SSH transport tests also cover target substitution, source and result substitution, output and diagnostic limits, failed exit, lost acknowledgement and cancellation after partial output. Existing source-transfer tests protect the shared stream-exchange behavior.

An opt-in Docker integration builds both helpers into a disposable layer over an exact image. It uses a controlled SSH peer to export real Linux binary changes, recover a lost acknowledgement, repeat the same artifact, reject an escaping link and verify unchanged workspace contents. The test owns the only writer. It does not establish live gateway authorization or a production quiescence mechanism. For example:

```sh
DATAGROUND_TEST_WORKSPACE_CHANGE_BASE_IMAGE=sha256:<local-image-digest> \
  go test ./internal/execution/openshell -run '^TestWorkspaceChangeDockerExport$' -count=1
```

Durable retention tests exercise real PostgreSQL claims and the artifact object-store boundary, including expiry before a write and after a successful write, lost acknowledgements, audit rollback, replacement-worker recovery, concurrent historical replay, scope substitution, append-only records, deletion and protected downgrade. The existing live source-grant fixture also selects this retention case against the pinned S3 candidate.

CI runs the Docker integration with the same pinned disposable base as the source-transfer test. The image layer is removed after the test and is not a published or accepted candidate.

This is a final difference, not an ordered file-write journal. Cedar export authority, a verified workload quiescence mechanism, accepted image installation, worker integration of durable retention, export before teardown, `termination_blocked` recovery, repository/base-revision metadata, ordered journals and checkpoints remain separate work. No production or release acceptance is established here.
