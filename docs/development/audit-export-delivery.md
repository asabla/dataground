# Audit export encrypted delivery lifecycle

The internal `dataground-audit-export-delivery` command records the durable lifecycle around an audit transport. DataGround prepares one isolation-scoped delivery for an exact [recipient-encrypted package](audit-export-encryption.md) only while its recipient profile and encryption key are the latest active, identity-proven [trust generation](audit-export-recipient-trust.md). New version 5 deliveries require a completed [immutable development transport](audit-export-transport.md) before the command can verify and record a recipient-signed acknowledgement against the same generation.

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

Execute the exact prepared package through the transport command, then require the configured recipient to authenticate and decrypt it, verify the embedded signed envelope, and return the closed Ed25519 version 4 receipt described in [recipient acknowledgement receipts](audit-export-delivery-receipts.md). Record that exact receipt with the same immutable files and a new actor/reason/correlation attribution:

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

Exact preparation, transport, and acknowledgement replays are read-only. Reusing a delivery identifier, operation correlation, envelope or package identity, destination binding, attribution, receipt, recipient trust profile, either recipient key, or trust generation with different values fails closed. The database permits only operation-bound transitions under the exact prepared generation, rejects deletion, and keeps delivery, transport, operation, trust-event, and trust-key evidence append-only. Concurrent trust rotation, revocation, proof-revocation intake, preparation, transport reservation, and acknowledgement serialize around the same domain and recipient. Schema 28 preserves completed historical evidence and pending version 3 and 4 deliveries, while requiring version 5 transport evidence for every new preparation.

To acknowledge a pending historical version 4 delivery, use its original encrypted package and version 3 receipt and add `-delivery-contract dataground.audit-export-delivery/v4`. Historical acknowledgement retains its original pre-transport claim and does not create version 5 transport evidence.

An acknowledged version 5 row proves that DataGround prepared the exact recipient-encrypted package under the then-current authorized X25519 key, verified the same bytes at the bound immutable development object, and accepted a receipt signed by a key from the same trust generation. It also proves that the generation's proof remained unexpired and outside the effective recorded revocation set at preparation, transport reservation, and acknowledgement. It does not prove recipient plaintext retention, authenticated production transport, external authority process quality, or plaintext source erasure. Authenticated remote transport, proofing- and revocation-authority governance, external evidence and notice acquisition, recipient access, retention, legal hold, secure deletion, and independent cryptographic-policy review remain deployment boundaries. Historical version 4 acknowledgements retain their narrower pre-transport claim.
