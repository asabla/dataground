# Audit export delivery receipts

The version 2 delivery acknowledgement boundary accepts only a canonical `dataground.audit-export-delivery-receipt/ed25519/v1` document signed by a key in an immutable `dataground.audit-export-recipient-trust/ed25519/v1` profile. The receipt binds the delivery contract and identifier, isolation domain, export kind and identifier, envelope and export digests, export trust profile and signing key, recipient, destination digest, and recipient-reported acceptance time. The runtime requires UTC with no finer than microsecond precision; it records that external time separately from the database acknowledgement time and does not treat it as a trusted clock.

The recipient trust profile contains the exact recipient identifier and one to eight sorted Ed25519 keys. Historical profiles must remain available with their receipts because the receipt binds the complete canonical profile digest:

```json
{"contract":"dataground.audit-export-recipient-trust/ed25519/v1","recipientId":"archive.primary","keys":[{"keyId":"archive_key_01","publicKey":"REPLACE_WITH_UNPADDED_BASE64URL_PUBLIC_KEY"}]}
```

The receipt `contentSha256` is SHA-256 over the compact canonical `content` object. The signing input is the ASCII domain `DataGround audit export delivery receipt v1`, a newline, then one compact JSON object with fields in this exact order: `contract`, `content`, `contentSha256`, `recipientTrustProfileSha256`, and `keyId`, followed by one newline. The nested `content` fields use the order published in `contracts/schemas/audit-export-delivery-receipt.schema.json`. Sign those exact bytes as raw Ed25519 input and place the unpadded base64url signature in the receipt's `signature` object.

Both the receipt and trust profile must be duplicate-free one-line canonical JSON files with one final newline, mode `0600`, beneath owner-only directories. The verifier rejects symlinks, path collisions, mutable parent directories, unknown fields, alternate digest encodings, unsorted or duplicate trust keys, cross-kind export identifiers, substituted delivery fields, sub-microsecond timestamps, and signatures from keys absent from the exact bound profile. The canonical schemas and fixtures are under `contracts/schemas` and `contracts/fixtures`.

Verification authenticates which pinned recipient key approved the exact delivery binding. Recipient identity proofing, key authorization, rotation and revocation, transport execution, encryption, access, retention, legal hold, deletion, and independent clock policy remain deployment responsibilities.
