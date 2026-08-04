# Audit export encrypted delivery acknowledgement

The internal `dataground-audit-export-delivery` command records the durable lifecycle around a deployment-owned audit transport. DataGround prepares one isolation-scoped delivery for an exact [recipient-encrypted package](audit-export-encryption.md) only while its recipient profile and encryption key are the latest active, identity-proven [trust generation](audit-export-recipient-trust.md). It later verifies a recipient-signed acknowledgement against the same generation before recording its digest and signer provenance. It does not upload, route, or disclose the package.

Preparation binds the delivery identifier, export kind and identifier, complete envelope and encrypted-package digests, export trust-profile digest and signing key, recipient trust-profile digest and X25519 key, the exact database trust generation, deployment-owned recipient identifier, and a SHA-256 digest of the deployment's immutable destination binding. PostgreSQL checks proof expiry and effective external revocations using its own clock, then records the operator, reason digest, and correlation in an append-only operation before returning success. The destination binding remains outside DataGround and must not contain reusable credentials.

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-delivery \
  -operation prepare \
  -delivery-id adl_00000000000000000001 \
  -isolation-domain iso_00000000000000000001 \
  -envelope-file /run/dataground/audit/sealed-export.json \
  -encrypted-file /run/dataground/audit/encrypted-export.json \
  -trust-file /run/dataground/audit/export-trust.json \
  -recipient-trust-file /run/dataground/audit/archive-trust.json \
  -recipient archive.primary \
  -destination-sha256 sha256:REPLACE_WITH_DESTINATION_BINDING_DIGEST \
  -actor operator@example.invalid \
  -reason 'deliver incident export to the reviewed archive' \
  -correlation-id cor_00000000000000000001
```

The deployment transport must send the exact encrypted package whose digest was prepared. Require the configured recipient to authenticate and decrypt it, verify the embedded signed envelope, and return the closed Ed25519 receipt described in [recipient acknowledgement receipts](audit-export-delivery-receipts.md). Record that exact receipt with the same immutable files and a new actor/reason/correlation attribution:

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-delivery \
  -operation acknowledge \
  -delivery-id adl_00000000000000000001 \
  -isolation-domain iso_00000000000000000001 \
  -envelope-file /run/dataground/audit/sealed-export.json \
  -encrypted-file /run/dataground/audit/encrypted-export.json \
  -trust-file /run/dataground/audit/export-trust.json \
  -recipient archive.primary \
  -destination-sha256 sha256:REPLACE_WITH_DESTINATION_BINDING_DIGEST \
  -receipt-file /run/dataground/audit/archive-receipt.json \
  -recipient-trust-file /run/dataground/audit/archive-trust.json \
  -actor operator@example.invalid \
  -reason 'record reviewed archive acknowledgement' \
  -correlation-id cor_00000000000000000002
```

Exact preparation and acknowledgement replays are read-only. Reusing a delivery identifier, operation correlation, envelope or package identity, destination binding, attribution, receipt, recipient trust profile, either recipient key, or trust generation with different values fails closed. The database permits only an operation-bound transition under the exact prepared generation, rejects deletion, and keeps delivery, operation, trust-event, and trust-key rows append-only. Concurrent trust rotation, revocation, proof-revocation intake, preparation, and acknowledgement serialize around the same domain and recipient. Schema 27 preserves completed historical evidence and pending version 3 deliveries, while requiring version 4 encryption evidence for every new preparation.

An acknowledged version 4 row proves that DataGround prepared the exact recipient-encrypted package under the then-current authorized X25519 key, that a signing key from the same trust generation signed the exact package and delivery binding, that the generation's proof remained unexpired and outside the effective recorded revocation set at both consequential transitions, and that authorized database operators recorded the canonical evidence. It does not independently prove that transport occurred, that the recipient retained plaintext after signing, that either external authority's process was sound, or that plaintext source files were erased. Transport execution and recovery, proofing- and revocation-authority governance, external evidence and notice acquisition, authenticated recipient access, retention, legal hold, secure deletion, and independent cryptographic-policy review remain deployment boundaries.
