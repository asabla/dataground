# Audit export transport

The internal `dataground-audit-export-transport` command executes the transport boundary for a prepared version 5 audit delivery. It reserves one exact attributed operation and transport contract in PostgreSQL, conditionally writes the recipient-encrypted package to the destination's deterministic immutable S3 object, reads the object back and verifies the complete bytes, then records transport completion. A missing, different, oversized, encoded, or unreadable object fails closed and cannot be replaced by this command.

The canonical destination document binds the delivery, isolation domain, recipient, transport contract, S3 origin, bucket, addressing style, and object key. It contains no credentials. Its SHA-256 digest is the `destinationSha256` already covered by preparation and the recipient receipt. The object key must be `audit-export-deliveries/v1/{isolationDomainId}/{deliveryId}/{encrypted-package-hex-digest}.json`.

The original `dataground.audit-export-delivery-destination/s3/v1` profile remains deliberately limited to anonymous path-style S3 on an explicitly enabled plaintext IP-loopback endpoint. The authenticated `dataground.audit-export-delivery-destination/s3-mtls/v2` profile instead requires HTTPS, an exact client certificate chain and private key, and a pinned server CA bundle. Its destination binds SHA-256 digests of the exact client certificate file and server trust bundle and selects `dataground.audit-export-transport/s3-immutable-mtls/v2`; the private key is never placed in the destination or PostgreSQL. Both profiles disable environment proxies and redirects.

The mTLS files must use absolute canonical paths, be distinct mode-`0600` regular files in the same or separate mode-`0700` owner-owned directories, and remain the same inode, owner, size, mode, and modification time while read. The client leaf must be currently valid, non-CA, and authorized for TLS client authentication. Only CA certificates are accepted in the pinned server bundle, system roots are not added, and TLS 1.2 is the minimum. The certificate and trust-bundle bytes must match the destination digests before any database reservation or object request.

The destination file must be duplicate-free one-line canonical JSON with one final newline, mode `0600`, beneath an owner-only directory. Its object-key suffix must be the lowercase hexadecimal SHA-256 digest of the exact encrypted-package bytes. The checked fixtures at `contracts/fixtures/valid/audit-export-delivery-destination.json` and `contracts/fixtures/valid/audit-export-delivery-destination-mtls.json` show both field sets; canonicalize the deployment-specific document before computing and supplying `destinationSha256`.

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
  -actor operator@example.invalid \
  -reason 'transport incident export to the reviewed archive object' \
  -correlation-id cor_00000000000000000003 \
  -allow-loopback-http
```

Reservation and completion are exact replays. A process failure before the object write leaves a recoverable reservation. A lost or conflicting write acknowledgement is resolved by exact read-back, so a retry can converge without replacing the object. A database failure after verified publication leaves the immutable object available for the same retry to observe before completion. Changed package bytes, destination binding, attribution, correlation, or durable delivery state fail closed. Recipient acknowledgement remains a separate signed operation and is rejected until version 5 transport completion exists.

For an mTLS destination, omit `-allow-loopback-http` and add `-client-certificate-file`, `-client-private-key-file`, and `-server-trust-bundle-file`. Exact retries must reuse the same destination, transport contract, identity and trust digests, evidence, and attribution. Rotation therefore requires a newly prepared delivery and destination rather than silently changing identity beneath an existing reservation. If the bound client certificate expires before completion, the command leaves the existing reservation incomplete and refuses further effects; operators must prepare a new delivery under current identity material rather than upgrade the old evidence.

The required S3 conformance job exercises the immutable adapter against the pinned disposable SeaweedFS candidate, including missing-object handling, immutable creation, complete read-back, and exact replay. That live evidence remains limited to the anonymous loopback topology. Focused tests prove a real mutually authenticated TLS handshake, pinned server roots, exact client-certificate binding, invalid-purpose rejection, and owner-only private-key enforcement. Workload-identity issuance and rotation, certificate revocation, server authorization and bucket policy, DNS governance, KMS, object lock, retention, multi-gateway behavior, and production deployment certification remain operator and release boundaries.
