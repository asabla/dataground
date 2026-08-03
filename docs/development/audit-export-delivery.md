# Audit export delivery acknowledgement

The internal `dataground-audit-export-delivery` command records the durable lifecycle around a deployment-owned audit transport. DataGround prepares one isolation-scoped delivery for an exact cryptographically verified audit-export envelope and later records one external acknowledgement digest. It does not upload, encrypt, route, or disclose the export.

Preparation binds the delivery identifier, export kind and identifier, complete envelope digest, embedded export digest, trust-profile digest, signing key, deployment-owned recipient identifier, and a SHA-256 digest of the deployment's immutable destination binding. PostgreSQL records the operator, reason digest, and correlation in an append-only operation before returning success. The destination binding remains outside DataGround and must not contain reusable credentials.

```sh
DATAGROUND_DATABASE_URL='postgres://dataground:dataground@127.0.0.1:5432/dataground?sslmode=disable' \
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

The deployment transport must send the exact envelope whose digest was prepared. After the external system returns stable acknowledgement evidence, record the SHA-256 digest of that evidence with a new actor/reason/correlation attribution:

```sh
DATAGROUND_DATABASE_URL='postgres://dataground:dataground@127.0.0.1:5432/dataground?sslmode=disable' \
go run ./cmd/dataground-audit-export-delivery \
  -operation acknowledge \
  -delivery-id adl_00000000000000000001 \
  -isolation-domain iso_00000000000000000001 \
  -envelope-file /run/dataground/audit/sealed-export.json \
  -trust-file /run/dataground/audit/trust.json \
  -recipient archive.primary \
  -destination-sha256 sha256:REPLACE_WITH_DESTINATION_BINDING_DIGEST \
  -acknowledgement-sha256 sha256:REPLACE_WITH_ACKNOWLEDGEMENT_EVIDENCE_DIGEST \
  -actor operator@example.invalid \
  -reason 'record reviewed archive acknowledgement' \
  -correlation-id cor_00000000000000000002
```

Exact preparation and acknowledgement replays are read-only. Reusing a delivery identifier, operation correlation, envelope identity, destination binding, attribution, or acknowledgement with different values fails closed. The database permits only the prepared-to-acknowledged transition, rejects deletion, and keeps both operation rows append-only. The safe operator-audit stream exposes only reviewed identifiers and digests.

An acknowledged row proves that an authorized database operator recorded the supplied external evidence digest for the exact prepared envelope. It does not independently prove that the transport sent those bytes, that the recipient accepted or retained them, or that the export was encrypted. Transport execution and recovery, encryption, authenticated recipient access, acknowledgement verification policy, retention, legal hold, deletion, and trust revocation remain deployment boundaries.
