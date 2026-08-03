# Audit export delivery acknowledgement

The internal `dataground-audit-export-delivery` command records the durable lifecycle around a deployment-owned audit transport. DataGround prepares one isolation-scoped delivery for an exact cryptographically verified audit-export envelope and later verifies a recipient-signed acknowledgement before recording its digest and signer provenance. It does not upload, encrypt, route, or disclose the export.

Preparation binds the delivery identifier, export kind and identifier, complete envelope digest, embedded export digest, trust-profile digest, signing key, deployment-owned recipient identifier, and a SHA-256 digest of the deployment's immutable destination binding. PostgreSQL records the operator, reason digest, and correlation in an append-only operation before returning success. The destination binding remains outside DataGround and must not contain reusable credentials.

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-delivery \
  -operation prepare \
  -delivery-id adl_00000000000000000001 \
  -isolation-domain iso_00000000000000000001 \
  -envelope-file /run/dataground/audit/sealed-export.json \
  -trust-file /run/dataground/audit/trust.json \
  -recipient archive.primary \
  -destination-sha256 sha256:REPLACE_WITH_DESTINATION_BINDING_DIGEST \
  -actor operator@example.invalid \
  -reason 'deliver incident export to the reviewed archive' \
  -correlation-id cor_00000000000000000001
```

The deployment transport must send the exact envelope whose digest was prepared. The configured recipient must return the closed Ed25519 receipt described in [recipient acknowledgement receipts](audit-export-delivery-receipts.md). Record that exact receipt with its immutable recipient trust profile and a new actor/reason/correlation attribution:

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-delivery \
  -operation acknowledge \
  -delivery-id adl_00000000000000000001 \
  -isolation-domain iso_00000000000000000001 \
  -envelope-file /run/dataground/audit/sealed-export.json \
  -trust-file /run/dataground/audit/trust.json \
  -recipient archive.primary \
  -destination-sha256 sha256:REPLACE_WITH_DESTINATION_BINDING_DIGEST \
  -receipt-file /run/dataground/audit/archive-receipt.json \
  -recipient-trust-file /run/dataground/audit/archive-trust.json \
  -actor operator@example.invalid \
  -reason 'record reviewed archive acknowledgement' \
  -correlation-id cor_00000000000000000002
```

Exact preparation and acknowledgement replays are read-only. Reusing a delivery identifier, operation correlation, envelope identity, destination binding, attribution, receipt, recipient trust profile, or signing key with different values fails closed. The database permits only the prepared-to-acknowledged transition, rejects deletion, and keeps both operation rows append-only. The safe operator-audit stream exposes only reviewed identifiers and digests. Schema 23 preserves completed v1 opaque acknowledgements as legacy evidence and upgrades pending v1 deliveries to v2; it never reinterprets a completed legacy acknowledgement as cryptographically verified.

An acknowledged v2 row proves that the pinned recipient key signed the exact prepared delivery binding and that an authorized database operator recorded that canonical receipt. It does not independently prove that the transport sent the envelope, that the recipient retained it after signing, or that the export was encrypted. Transport execution and recovery, authenticated recipient access, key authorization and revocation, retention, legal hold, and deletion remain deployment boundaries.
