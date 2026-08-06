# Audit export transport

The internal `dataground-audit-export-transport` command executes the transport boundary for a prepared version 6 audit delivery. It reserves one exact attributed operation, transport contract, and [workload identity](audit-export-workload-identity.md) generation in PostgreSQL, conditionally writes the recipient-encrypted package to the destination's deterministic immutable S3 object, reads the object back and verifies the complete bytes, then records transport completion. A missing, different, oversized, encoded, or unreadable object fails closed and cannot be replaced by this command.

The canonical destination document binds the delivery, isolation domain, recipient, transport contract, S3 origin, bucket, addressing style, and object key. It contains no credentials. Its SHA-256 digest is the `destinationSha256` already covered by preparation and the recipient receipt. The object key must be `audit-export-deliveries/v1/{isolationDomainId}/{deliveryId}/{encrypted-package-hex-digest}.json`.

The original `dataground.audit-export-delivery-destination/s3/v1` and `dataground.audit-export-delivery-destination/s3-mtls/v2` profiles remain frozen for historical version 5 delivery replay. New preparation requires `dataground.audit-export-delivery-destination/s3-mtls-workload/v3`, HTTPS, an exact client certificate chain and private key, and a pinned server CA bundle. The destination binds SHA-256 digests of the exact client certificate file and server trust bundle plus the workload identifier, signed grant digest, and sequential PostgreSQL authorization generation, and selects `dataground.audit-export-transport/s3-immutable-mtls-workload/v3`. The private key is never placed in the destination, signed grant, or PostgreSQL. All profiles disable environment proxies and redirects.

The mTLS files must use absolute canonical paths, be distinct mode-`0600` regular files in the same or separate mode-`0700` owner-owned directories, and remain the same inode, owner, size, mode, and modification time while read. The client leaf must be currently valid, non-CA, and authorized for TLS client authentication. Only CA certificates are accepted in the pinned server bundle, system roots are not added, and TLS 1.2 is the minimum. The certificate and trust-bundle bytes must match the destination digests before any database reservation or object request.

The destination file must be duplicate-free one-line canonical JSON with one final newline, mode `0600`, beneath an owner-only directory. Its object-key suffix must be the lowercase hexadecimal SHA-256 digest of the exact encrypted-package bytes. The current checked fixture is `contracts/fixtures/valid/audit-export-delivery-destination-workload.json`; the original loopback and mTLS fixtures remain frozen for historical replay. Canonicalize the deployment-specific document before computing and supplying `destinationSha256`.

For a canonical destination at `/run/dataground/audit/archive-destination.json`, execute the exact prepared delivery with the same evidence and attribution on every retry:

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-transport \
  -delivery-id adl_00000000000000000001 \
  -isolation-domain iso_00000000000000000001 \
  -envelope-file /run/dataground/audit/sealed-export.json \
  -encrypted-file /run/dataground/audit/encrypted-export.json \
  -trust-file /run/dataground/audit/export-trust.json \
  -recipient-trust-file /run/dataground/audit/archive-trust.json \
  -recipient archive.primary \
  -destination-file /run/dataground/audit/archive-destination.json \
  -destination-sha256 sha256:REPLACE_WITH_DESTINATION_DOCUMENT_DIGEST \
  -client-certificate-file /run/dataground/audit/client-chain.pem \
  -client-private-key-file /run/dataground/audit/client-key.pem \
  -server-trust-bundle-file /run/dataground/audit/server-ca.pem \
  -workload-identity-grant-file /run/dataground/audit/workload-grant.json \
  -workload-identity-trust-file /run/dataground/audit/workload-issuer-trust.json \
  -actor operator@example.invalid \
  -reason 'transport incident export to the reviewed archive object' \
  -correlation-id cor_00000000000000000003
```

Reservation and completion are exact replays. A process failure before the object write leaves a recoverable reservation. While the bound workload identity remains current, a lost or conflicting write acknowledgement is resolved by exact read-back, so a retry can converge without replacing the object; a database failure after verified publication likewise leaves the immutable object available for the same retry to observe before completion. If the identity rotates, expires, or is locally revoked before a reserved retry, the command will not repeat or observe the object effect under stale authority; the incomplete reservation remains immutable evidence and operators must prepare a new delivery. Changed package bytes, destination binding, identity, attribution, correlation, or durable delivery state fail closed. Recipient acknowledgement remains a separate signed operation and is rejected until the matching version 6 workload-authorized transport completion exists.

For the current workload-authorized destination, omit `-allow-loopback-http` and add `-client-certificate-file`, `-client-private-key-file`, `-server-trust-bundle-file`, `-workload-identity-grant-file`, and `-workload-identity-trust-file`. Exact retries must reuse the same destination, transport contract, identity authorization, trust digests, evidence, and attribution. Rotation therefore requires a newly prepared delivery and destination rather than silently changing identity beneath an existing reservation. If the bound client certificate or signed grant expires before reservation, the command refuses the effect; operators must prepare a new delivery under current identity material rather than upgrade old evidence.

The required S3 conformance job exercises the immutable adapter against the pinned disposable SeaweedFS candidate, including missing-object handling, immutable creation, complete read-back, and exact replay. That live evidence remains limited to the historical anonymous loopback topology. Focused tests prove a real mutually authenticated TLS handshake, pinned server roots, exact client-certificate binding, invalid-purpose rejection, owner-only private-key enforcement, signed workload-grant verification, and database-enforced activation, rotation, expiry, revocation, and direct-write denial. External credential issuance and issuer-side revocation, server authorization and bucket policy, DNS governance, KMS, object lock, retention, multi-gateway behavior, and production deployment certification remain operator and release boundaries.
