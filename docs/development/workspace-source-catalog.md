# Workspace source catalog

The internal source installer records one reviewed archive under an exact isolation domain, workspace, and source identity. It uses the same bounded tar inspection as the [sandbox materializer](workspace-source-materialization.md), retains the original archive in immutable object storage, and commits separate preparation and completion records in PostgreSQL. The executable command supports an explicit loopback development store. The database credential is its administrative authorization boundary. It does not grant a session access to the source and is not a public upload API.

## Installation contract

`dataground.workspace-source/v1` binds the domain, workspace, source ID, archive digest and byte count, normalized entry and source-byte counts, and storage-profile digest. Actor and correlation are immutable installation attribution. Source identity is scoped by both domain and workspace. The storage profile is SHA-256 over compact JSON with these fields in this order, with no trailing newline:

```json
{"schemaVersion":"dataground.workspace-source-storage/v1","endpoint":"http://127.0.0.1:8333","bucket":"dataground-conformance","addressingStyle":"path"}
```

The endpoint, bucket, addressing mode, archive bytes, scope, identity, actor, and correlation must remain unchanged for an exact retry. The digest prevents a retry from silently selecting a different storage location. It does not certify the storage service or its operator. Object keys are derived under `workspace-sources/v1` from the exact domain, workspace, source ID, and archive digest. They remain internal routing data and are not public content URLs.

The installer validates the entire archive before database or object access. It locks the exact existing workspace, records an immutable preparation, and commits its audit and outbox event before accessing storage. A missing or foreign workspace fails closed. Storage writes use `If-None-Match: *` and the SHA-256 checksum. The installer reads and compares the complete stored archive after every write outcome, including a lost acknowledgement. It then appends completion with its audit and outbox event in one transaction. The finite installation states are pending and ready; completion cannot precede preparation.

An exact retry observes the retained object before any new write. Changed metadata or attribution conflicts. A ready source with missing or changed storage fails closed; it is not silently recreated. An audit or completion failure can leave a pending source and a retained object. Retry the exact installation to observe and complete it. A lost database commit acknowledgement is also ambiguous and requires the same retry. No list, overwrite, or delete storage authority is exposed by this adapter. Schema migration 65 rejects downgrade while either source table contains records. Both tables reject updates and deletes; retention and deletion need a separate governed design.

## Development command

Migrate the database with the repository's migration command. The workspace must already exist in that database. Obtain the archive from a reviewed source acquisition process, independently record its digest, and store it as an owner-only direct regular file. The command rejects links, multiple hard links, shared permissions, special modes, files above the archive bound, duplicate flags, and non-loopback endpoints.

For the exact development storage profile shown above, calculate its digest with the pinned Node.js toolchain:

```shell
node --input-type=module -e 'import {createHash} from "node:crypto"; const profile={schemaVersion:"dataground.workspace-source-storage/v1",endpoint:"http://127.0.0.1:8333",bucket:"dataground-conformance",addressingStyle:"path"}; console.log("sha256:"+createHash("sha256").update(JSON.stringify(profile)).digest("hex"));'
```

Supply the reviewed values explicitly:

```shell
DATAGROUND_DATABASE_URL='<administrative-database-url>' \
go run ./cmd/dataground-workspace-source-install \
  --isolation-domain iso_00000000000000000001 \
  --workspace wsp_00000000000000000001 \
  --source src_00000000000000000001 \
  --archive-file /private/source.tar \
  --archive-digest 'sha256:<reviewed-archive-digest>' \
  --storage-endpoint http://127.0.0.1:8333 \
  --storage-bucket dataground-conformance \
  --storage-profile-digest 'sha256:<reviewed-profile-digest>' \
  --actor reviewed-operator \
  --correlation-id cor_00000000000000000001
```

The command accepts only literal loopback HTTP origins and path-style storage, disables proxies and redirects, uses a 30-second storage request timeout, and uses a two-minute operation context. It acquires no storage or repository credentials. Standard output contains the completed source record. Failure emits only `WORKSPACE_SOURCE_INSTALLATION_UNAVAILABLE` and a nonzero exit; that diagnostic does not imply rollback. Source filenames, bytes, database diagnostics, and storage responses are not emitted.

## Verification and remaining work

The database integration test covers concurrent retries, storage acknowledgement loss, process replacement through a new repository instance, exact replay, cross-domain and workspace rejection, independent colliding identities, changed attribution, object drift, atomic completion audit failure, append-only records, and downgrade refusal. Unit tests cover preflight rejection, unsafe source files, storage-profile changes, namespace enforcement, redirects, response encoding, and size bounds. CI also runs the composed source recovery test against the pinned disposable S3 candidate and PostgreSQL, including a lost completion acknowledgement and a rejected object replacement. These checks establish those development boundaries only.

[Scoped source-use grants and verified reads](workspace-source-access.md) now provide a separate internal read boundary. Cedar entry and effect authorization for public source operations, repository provenance and credential mediation, invocation claim binding for source reads, sandbox transfer and image integration, source metadata for submodules and LFS, checkpoints, export, retention, deletion, and production storage certification remain required. An installation receipt is not source-use authority or v1.0 release acceptance.
