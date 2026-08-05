# Audit export signing

The internal `dataground-audit-export-seal` command can wrap either receipt-bound audit export in a detached-key Ed25519 envelope. The command validates the complete closed export contract, cursor progression, canonical UTC record times, content digest, export kind, and exact canonical JSON before it prepares any signing material. It never generates, loads, or stores a private key.

Every input is an absolute canonical regular-file path beneath an owner-only directory. Input files and generated files use mode `0600`; JSON inputs are duplicate-free, one-line canonical JSON with one final newline. The command rejects symlinks, path collisions, mutable parent directories, unknown fields, unsupported export versions, inconsistent cursor state, invalid content digests, and non-canonical encodings.

Create a closed trust profile containing one to eight sorted Ed25519 public keys. Keep each historical profile with the envelopes that bind its digest:

```json
{"contract":"dataground.audit-export-trust/ed25519/v1","keys":[{"keyId":"audit_key_01","publicKey":"REPLACE_WITH_UNPADDED_BASE64URL_PUBLIC_KEY"}]}
```

Prepare the exact message for the deployment's reviewed HSM, KMS, or offline signing process:

```sh
go run ./cmd/dataground-audit-export-seal \
  -export-file /run/dataground/audit/operator-export.json \
  -trust-file /run/dataground/audit/trust.json \
  -signing-message-file /run/dataground/audit/signing-message.bin
```

The message is the ASCII domain `DataGround audit export envelope v1`, a newline, the fixed export kind, a newline, the exact trust-profile digest, a newline, and the complete canonical export bytes. Sign that file as raw Ed25519 input. Place the resulting 64-byte signature in a closed canonical file using unpadded base64url:

```json
{"contract":"dataground.audit-export-signature/ed25519/v1","keyId":"audit_key_01","signature":"REPLACE_WITH_UNPADDED_BASE64URL_SIGNATURE"}
```

Verify the signature and install the immutable envelope:

```sh
go run ./cmd/dataground-audit-export-seal \
  -export-file /run/dataground/audit/operator-export.json \
  -signature-file /run/dataground/audit/signature.json \
  -trust-file /run/dataground/audit/trust.json \
  -output-file /run/dataground/audit/sealed-export.json
```

An identical install is an idempotent replay; different bytes at the same output path fail closed. Verify an installed envelope independently with `-verify-file /run/dataground/audit/sealed-export.json -trust-file /run/dataground/audit/trust.json`. The canonical envelope contract is `contracts/schemas/audit-export-envelope.schema.json`.

The signature authenticates the exact export bytes and the reviewed trust profile. It does not prove to a verifier without database access that the local export receipt exists, and the envelope itself remains plaintext. Signing authorization, private-key lifecycle and signing-system audit remain deployment-owned. The separate [encryption command](audit-export-encryption.md) can protect that exact envelope for an identity-proven X25519 recipient, and the [delivery lifecycle](audit-export-delivery.md) can bind the encrypted package to a deployment-owned destination and the same active [recipient trust generation](audit-export-recipient-trust.md), verify it at one immutable loopback or pinned-mTLS S3 object, and require a [recipient-signed receipt](audit-export-delivery-receipts.md). Production transport certification and workload-identity issuance, proofing- and revocation-authority governance, external notice acquisition, access policy, retention, legal hold, and deletion remain separate boundaries.
