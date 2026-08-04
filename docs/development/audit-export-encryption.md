# Audit export encryption

The internal `dataground-audit-export-encrypt` command encrypts one verified signed audit-export envelope to one X25519 key from an identity-proven recipient trust profile. The resulting canonical `dataground.audit-export-encrypted-package/x25519-aes256gcm/v1` document binds the envelope digest, export identity and isolation domain, recipient, complete recipient trust-profile digest, encryption key, ephemeral public key, and nonce as authenticated data. Its ciphertext contains the exact canonical envelope, including the export signature.

Recipient profiles used for encryption have contract `dataground.audit-export-recipient-trust/ed25519-x25519/v2` and separate sorted Ed25519 acknowledgement keys and X25519 encryption keys:

```json
{"contract":"dataground.audit-export-recipient-trust/ed25519-x25519/v2","recipientId":"archive.primary","signingKeys":[{"keyId":"archive_signing_key_01","publicKey":"REPLACE_WITH_UNPADDED_BASE64URL_ED25519_PUBLIC_KEY"}],"encryptionKeys":[{"keyId":"archive_encryption_key_01","publicKey":"REPLACE_WITH_UNPADDED_BASE64URL_X25519_PUBLIC_KEY"}]}
```

Encrypt an installed envelope only after the exact profile has been activated through the [recipient trust registry](audit-export-recipient-trust.md):

```sh
go run ./cmd/dataground-audit-export-encrypt \
  -envelope-file /run/dataground/audit/sealed-export.json \
  -trust-file /run/dataground/audit/export-trust.json \
  -recipient-trust-file /run/dataground/audit/archive-trust.json \
  -recipient-encryption-key archive_encryption_key_01 \
  -output-file /run/dataground/audit/encrypted-export.json
```

The command verifies and re-hashes the envelope and both trust profiles, generates a fresh ephemeral X25519 key and 96-bit nonce, derives an AES-256 key through HKDF-SHA-256, and installs the package once as an owner-only file. Repeating encryption intentionally produces a different package; immutable installation prevents overwriting an earlier package. Inspect an installed package's canonical structure and exact public bindings with `-verify-file` and the same envelope and trust files. Only a holder of the selected recipient private key can authenticate and decrypt its ciphertext.

For recipient interoperability, decode `ephemeralPublicKey`, `nonce`, and `ciphertext` as unpadded base64url. Derive the X25519 shared secret with the recipient private key. The authenticated data is one compact JSON object followed by a newline, with fields in this exact order: `contract`, `envelopeSha256`, `exportKind`, `exportId`, `isolationDomainId`, `recipientId`, `recipientTrustProfileSha256`, `encryptionKeyId`, `ephemeralPublicKey`, and `nonce`. Derive 32 bytes with RFC 5869 HKDF-SHA-256 using SHA-256 of those authenticated-data bytes as salt and `DataGround audit export encrypted package v1\n` as info. AES-256-GCM opens the ciphertext with the decoded 12-byte nonce and those same authenticated-data bytes; the ciphertext includes the 16-byte GCM tag.

All input and output paths must be distinct canonical regular files beneath owner-only directories. The command rejects symlinks, mutable parent directories, legacy signing-only recipient profiles, unknown or substituted keys, cross-domain or cross-export bindings, non-canonical JSON, malformed unpadded base64url values, and packages larger than 100 MiB. Private key generation, custody, rotation, recipient decryption, and independent algorithm-policy review remain deployment-owned.

The [delivery lifecycle](audit-export-delivery.md) records the exact encrypted-package digest and the database-authorized recipient trust generation before any external transfer. Encryption provides confidentiality and integrity for the package bytes; it does not execute transport, prove receipt, erase the plaintext input files, or govern access and retention after decryption.
