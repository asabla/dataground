# Audit export transport

The internal `dataground-audit-export-transport` command executes the development transport boundary for a prepared version 5 audit delivery. It reserves one exact attributed operation in PostgreSQL, conditionally writes the recipient-encrypted package to the destination's deterministic immutable S3 object, reads the object back and verifies the complete bytes, then records transport completion. A missing, different, oversized, encoded, or unreadable object fails closed and cannot be replaced by this command.

The canonical destination document binds the delivery, isolation domain, recipient, transport contract, S3 origin, bucket, addressing style, and object key. It contains no credentials. Its SHA-256 digest is the `destinationSha256` already covered by preparation and the recipient receipt. The object key must be `audit-export-deliveries/v1/{isolationDomainId}/{deliveryId}/{encrypted-package-hex-digest}.json`.

The executable command profile is deliberately limited to anonymous path-style S3 on an explicitly enabled plaintext IP-loopback endpoint. This matches the disposable development backend and does not claim authenticated remote S3, workload identity, production transport, recipient access, or retention. The underlying adapter accepts an operator-owned HTTP transport as the future authentication seam; DataGround does not store transport credentials.

The destination file must be duplicate-free one-line canonical JSON with one final newline, mode `0600`, beneath an owner-only directory. Its object-key suffix must be the lowercase hexadecimal SHA-256 digest of the exact encrypted-package bytes. The checked fixture at `contracts/fixtures/valid/audit-export-delivery-destination.json` shows the field set; canonicalize the deployment-specific document before computing and supplying `destinationSha256`.

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

The required S3 conformance job exercises this adapter against the pinned disposable SeaweedFS candidate, including missing-object handling, immutable creation, complete read-back, and exact replay. That evidence is limited to the anonymous loopback development topology.
