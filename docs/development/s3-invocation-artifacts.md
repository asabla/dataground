# S3 invocation-artifact conformance

The disposable S3 development profile exercises the concrete invocation-artifact transport through the same explicit operator-owned transport used by enforcement objects. The conformance command constructs the opt-in artifact store with a fixed test-only one-megabyte bound and uses only deterministic keys under `invocation-artifacts/v1`.

The live cases require a missing read to return the stable missing outcome, an exact conditional create to read back byte-for-byte, an existing object to reject replacement, an oversized body to fail before transport, and two synchronized writers to produce exactly one durable winner. The suite has no list, delete, bucket-provisioning, credential, public-URL, or lifecycle authority. It runs only against the caller-provisioned disposable bucket and emits no endpoint, bucket, key, content, credential, or upstream error detail.

Run it through the existing disposable profile:

```shell
docker compose -f deploy/storage/seaweedfs-conformance.yml up -d
go run ./cmd/dataground-s3-conformance \
  --endpoint http://127.0.0.1:8333 \
  --bucket dataground-conformance \
  --addressing-style path \
  --run-id 0123456789abcdef0123456789abcdef \
  --allow-loopback-http
docker compose -f deploy/storage/seaweedfs-conformance.yml down --volumes
```

The CI profile additionally composes the real bounded artifact adapter, immutable finalizer, and PostgreSQL catalog under an exact reserved version-2 runtime claim. It deliberately loses the catalogue acknowledgement only after a durable bind, then requires concurrent identical retries to converge through read-only replay on one private object record, one public descriptor, and one audit record; conflicting bytes remain rejected.

This evidence certifies only the anonymous loopback development boundary against the pinned disposable candidates. It does not certify runtime export, workload authentication, encryption or KMS, retention, replication, backup and restore, upgrades, multi-gateway behavior, partitions, production topology, or backend selection.
