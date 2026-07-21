# S3 enforcement-object boundary

DataGround's enforcement-object adapter implements the smallest S3 REST subset needed by immutable execution-policy finalization and retrieval. It is an internal protocol adapter, not a general object-storage service and not evidence that any backend is production-ready.

The adapter targets one dedicated platform-object bucket. A caller supplies the endpoint, bucket, addressing style, and an explicit `http.RoundTripper`. The production transport must own workload authentication and TLS trust, but that behavior is not yet implemented or certified. DataGround does not store access keys, choose an identity mechanism, create buckets, follow redirects, accept endpoint user information, or permit remote plaintext HTTP. Loopback HTTP must be enabled explicitly for development tests.

Writes use a single `PutObject` request with `If-None-Match: *`, an exact content length, the enforcement media type, and a base64 SHA-256 checksum. The adapter owns and verifies the bounded body before sending it. A `412 Precondition Failed` response maps to the immutable-object conflict signal. Other failures, including `409 ConditionalRequestConflict`, remain ambiguous storage failures so the finalizer can recover by reading the deterministic key before deciding whether the candidate conflicts.

Reads use `GetObject` with identity content encoding. Only `200 OK` returns a stream and only `404 Not Found` maps to the missing-object signal. Redirects, authorization failures, malformed or oversized responses, transport failures, and every other status collapse to a safe unavailable error. Upstream response bodies, endpoint details, bucket names, and authentication failures do not cross the adapter.

The adapter contract tests prove path and virtual-hosted URL construction, conditional and checksummed requests, missing/conflict mapping, cancellation, body ownership, path-confusion rejection, redirect denial, endpoint validation, encoded-response rejection, and response-detail sanitization.

## Development conformance profile

The reusable suite under `internal/execution/s3conformance` operates only through the logical reader and writer ports. It uses a unique caller-supplied run scope and verifies a missing read, exact create/read, immutable replacement denial, and eight simultaneous candidates for one absent key. Exactly one concurrent candidate must succeed; every loser must return the stable conflict signal, and the winner must read back byte-for-byte.

The same live suite exercises the complete immutable finalizer over that backend. It drops one successful write acknowledgement and requires deterministic read-back recovery without a repeated write, rejects conflicting bytes without binding metadata, and injects one catalog failure before proving that the retained object is adopted safely on retry. The catalog used by these cases is deliberately process-local fault scaffolding; PostgreSQL transaction behavior remains covered by the separate repository integration tests. Reports contain only the schema, run identifier, case names, and pass/fail states. Endpoint, bucket, keys, policy bytes, credentials, catalog detail, and upstream errors are excluded.

The first live profile uses [SeaweedFS 4.40](https://github.com/seaweedfs/seaweedfs/releases/tag/4.40), pinned by its multi-architecture image digest and source commit. The version includes the upstream [atomic conditional-mutation change](https://github.com/seaweedfs/seaweedfs/pull/8802) that closed the concurrent `If-None-Match: *` defect. Its Apache-2.0 license, local single-binary mode, Kubernetes chart, and multi-architecture image make it a suitable first evidence candidate. It is not a production selection.

The disposable container drops every Linux capability, then restores only `CHOWN`, `SETGID`, and `SETUID` so the pinned image entrypoint can assign its temporary data directory and switch to its unprivileged runtime user. The profile check pins that exact set; it is startup plumbing, not storage authority or production hardening evidence.

The checked-in Compose profile is destructive only inside its disposable, memory-backed bucket and deliberately runs anonymous on loopback. Start a fresh instance for every run so the fixed evidence scope is absent:

```shell
docker compose -f deploy/storage/seaweedfs-conformance.yml up -d
go run ./cmd/dataground-s3-conformance \
  --endpoint http://127.0.0.1:8333 \
  --bucket dataground-conformance \
  --addressing-style path \
  --run-id 0123456789abcdef0123456789abcdef \
  --allow-loopback-http
docker compose -f deploy/storage/seaweedfs-conformance.yml down
```

The shipped runner accepts only an explicitly enabled plaintext literal-loopback endpoint, ignores environment proxy configuration, and has no list, delete, bucket-provisioning, or credential authority. Never point the reusable suite at a shared or retained bucket. A failure stops the suite at the first violated invariant and emits no backend detail. GitHub CI starts the digest-pinned candidate in a separate job and requires the live report to pass. Future remote profiles must construct the backend with an operator-owned authenticated transport instead of widening this development command.

The remaining production gate is intentionally broad. The live development evidence now includes immutable finalization recovery, but not PostgreSQL and object-store failure in one distributed test environment. Production still requires an operator-owned workload-authenticated transport and bucket policy; TLS, encryption/KMS, versioning, retention, multipart and presigned-operation behavior; multi-gateway concurrency and network partitions; replication, backup/restore, upgrades and rollback; Kubernetes operations and observability; a replacement backend; and the platform-object and lakehouse cases from ADR-035. None of those claims follow from this development subset.
