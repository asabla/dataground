# Workspace source materialization

The internal `workspace` package validates a complete source archive and installs its normalized tree in a new `workspace` directory. The `dataground-workspace-materialize` command exposes this operation to a future sandbox provisioning boundary. This is one prerequisite for specification section 9.7. It is not connected to the API, workspace resource, execution plan, OpenShell provider, or worker. It does not establish repository identity, authorization, a durable source catalog, or release acceptance.

## Input and normalization

The input is an uncompressed tar archive with an exact lowercase `sha256:` digest. Validation finishes before any filesystem change. The archive is limited to 32 MiB, regular-file content to 24 MiB, and materialized entries to 4,096. Inferred parent directories count as entries. At most 4,096 headers returned by the tar decoder are accepted, including global PAX metadata; extended header storage remains subject to the archive byte limit. Paths are bounded to 4,096 UTF-8 bytes, 64 components, and 255 bytes per component. The archive must have complete zero terminators and no trailing nonzero data.

Only regular files, directories, and contained relative symbolic links are accepted. Duplicate or noncanonical paths, absolute paths, traversal, control characters, backslashes, `.git` components, hard links, sparse files, special files, and special permission bits are rejected. A file or link cannot be an extraction parent. Link resolution follows filesystem component order, with bounded expansion; missing, escaping, or cyclic targets are rejected. All regular nodes are created before links. A Git archive global PAX `comment` is accepted as metadata but supplies no trusted commit identity.

Files retain their exact bytes, including binary content. A file with any executable bit becomes mode `0700`; other files become `0600`. Directories become `0700`. Archive ownership, timestamps, extended attributes, and other permissions are not installed. The resulting tree is writable by its owner. The digest identifies the input archive, not a Git revision or a post-edit tree. Different archives can normalize to the same tree.

## Provisioning and replay

Build the helper with the pinned Go toolchain:

```shell
go build -o .cache/bin/dataground-workspace-materialize ./cmd/dataground-workspace-materialize
```

The provisioning boundary must install a matching platform binary inside the sandbox. It supplies the archive through standard input and independently binds its digest. For example, inside that sandbox:

```shell
dataground-workspace-materialize \
  --archive-digest 'sha256:<64-lowercase-hex-digits>' \
  --destination /sandbox/workspace < source.tar
```

Only `/sandbox/workspace` and `/workspace` are accepted destinations. The parent must already exist, belong to the helper's effective user, and have no group or other write permission. The caller must keep the parent private to provisioning and the workspace inactive until installation or verification finishes. These are caller obligations; this helper has no authority to stop a workload or fence a worker. It does not mount a host checkout or acquire repository credentials.

On Linux and macOS, the package creates a private random staging directory under an open parent-directory handle, writes and syncs the tree, and atomically renames it with a no-replacement operation. Other platforms fail closed. An existing destination is never overwritten, including an empty directory or a symbolic link. The helper can recover a lost acknowledgement by reading back the entire existing tree. It requires the exact expected names, node types, owner, normalized modes, file bytes, and link targets, and rejects regular files with multiple hard links. Directory reads and file reads are bounded. Read-back establishes correspondence to the supplied normalized snapshot only; it cannot prove which archive originally created the tree or establish historical ownership.

Standard output contains only the versioned receipt, with archive digest, entry count, and source byte count. Failure produces the fixed diagnostic `WORKSPACE_SOURCE_MATERIALIZATION_FAILED` and a nonzero exit. Source paths and content are not included. The command bounds its lifetime to one minute and closes a blocked standard-input read on cancellation. Ordinary failures remove the current staging directory. A process crash can leave private staging state; a future durable provisioning controller must own its recovery and cleanup. A failure after publication can leave a complete workspace, which must be observed before any retry.

## Evidence and remaining integration

Focused package and command tests cover binary files, executable normalization, contained links, archive limits, malformed and escaping paths, link substitution, special modes, hard-link substitution, existing destinations, concurrent installers, staging cancellation, blocked input, and exact read-back. Linux and macOS use distinct no-replacement rename primitives. These tests do not establish OpenShell transfer, public workspace ownership, or production filesystem compatibility.

The source registry and immutable storage binding, repository credential broker, entry and effect authorization, isolation-domain ownership, durable provisioning leases, startup recovery, source transfer, image integration, base-revision metadata, submodules and LFS, change journal, checkpoints, export before teardown, and `termination_blocked` recovery remain required. No current invocation or session can claim those capabilities from this helper alone.
