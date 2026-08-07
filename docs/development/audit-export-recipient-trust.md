# Audit export recipient trust

The internal `dataground-audit-export-recipient-trust` command authorizes one exact recipient trust profile for one isolation domain and deployment-owned recipient only after verifying an external proofing authority's signed identity and key-custody statement. Encryption-capable profiles separate Ed25519 acknowledgement keys from X25519 package-encryption keys. PostgreSQL retains append-only sequential activation and revocation generations, the complete profile digest, both sorted key sets, minimized proof provenance and validity, operator attribution, and a reason digest. It does not store public-key bytes, private keys, proof, package, or receipt bytes, destination material, or transport credentials.

Activation requires the canonical owner-only recipient trust profile described in [audit export delivery receipts](audit-export-delivery-receipts.md), one canonical signed identity proof, and the exact owner-only proofing trust profile that authorizes its signer. An operator must first activate that exact profile and signing key through the isolation-scoped [proofing-authority registry](audit-export-proofing-authorities.md). The proof binds the isolation domain, recipient, recipient trust profile digest, proofing authority, an opaque SHA-256 digest of the authority's external evidence, and a UTC validity interval. The command validates and re-hashes all three files before opening PostgreSQL:

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-recipient-trust \
  -operation activate \
  -isolation-domain iso_00000000000000000001 \
  -recipient archive.primary \
  -generation 1 \
  -trust-file /run/dataground/audit/archive-trust.json \
  -identity-proof-file /run/dataground/audit/archive-identity-proof.json \
  -proofing-trust-file /run/dataground/audit/archive-proofing-trust.json \
  -actor operator@example.invalid \
  -reason 'authorize the reviewed archive receipt keys' \
  -correlation-id cor_00000000000000000001
```

The proof contract is `dataground.audit-export-recipient-identity-proof/ed25519/v1`; its schemas and fixtures are under `contracts/schemas` and `contracts/fixtures`. The proofing trust profile contract is `dataground.audit-export-recipient-proofing-trust/ed25519/v1` and binds one authority identifier to one to eight sorted Ed25519 keys. The proof `contentSha256` is SHA-256 over the compact canonical `content` object. Its signing input is the ASCII domain `DataGround audit export recipient identity proof v1`, a newline, and one compact JSON object containing `contract`, `content`, `contentSha256`, `proofingTrustProfileSha256`, and `keyId` in that order, followed by one newline.

The verifier requires canonical one-line JSON with a final newline, owner-only files and parent directories, exact unpadded base64url keys and signatures, UTC timestamps with no finer than microsecond precision, an unexpired validity interval, and a `verifiedAt` value no more than five minutes ahead of the command clock. It rejects path aliasing, substituted domains, recipients, trust profiles, evidence digests, authorities, signers, timestamps, and signatures. The evidence digest is only a durable correlation to an external proofing record; DataGround neither interprets that record nor infers a proofing method or assurance level.

Rotation activates a different canonical profile at the next generation. Repeating the active digest as a new activation is rejected because it would add attribution without changing authority. Revocation requires the next generation and the exact active profile digest, so an operator can remove authority even when the historical profile file is unavailable:

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-recipient-trust \
  -operation revoke \
  -isolation-domain iso_00000000000000000001 \
  -recipient archive.primary \
  -generation 2 \
  -trust-contract dataground.audit-export-recipient-trust/ed25519-x25519/v2 \
  -trust-sha256 sha256:REPLACE_WITH_ACTIVE_PROFILE_DIGEST \
  -actor operator@example.invalid \
  -reason 'revoke the archive receipt keys after custody change' \
  -correlation-id cor_00000000000000000002
```

Generations are gap-free per isolation domain and recipient. The first event must be generation 1 activation; revocation must match the active digest; a later activation may reauthorize the same digest only after revocation. One legacy version 1 activation may be upgraded in place conceptually by appending a version 2 activation for the same profile with valid identity proof; no new version 1 event is accepted. Exact replay is read-only, while changed attribution or proof provenance, correlation reuse, gaps, rollback, cross-domain reuse, unsorted or duplicate keys, and mutation or deletion of either event or key rows fail closed.

New activations require the encryption-capable version 2 trust profile and version 3 authorization evidence. Delivery preparation, transport reservation, and acknowledgement each resolve trust within the same PostgreSQL transaction and advisory scope as their durable transition. Preparation and transport reservation require the exact profile digest and X25519 key to belong to the latest identity-proven active generation. Acknowledgement additionally requires its Ed25519 key and pinned generation to match that exact preparation. All three transitions require the proof to remain unexpired according to the PostgreSQL clock and reject a matching effective [external proof revocation](audit-export-recipient-proof-revocation.md). Concurrent activation, rotation, revocation intake, preparation, transport reservation, or acknowledgement therefore completes before or after a consequential transition, never between authorization and its durable write. Later expiry or revocation does not rewrite completed evidence, and exact replay remains read-only.

Historical signing-only version 1 profiles and version 2 authorization events remain readable for pending version 3 acknowledgement and exact replay, but no new activation may use them. Existing identity-proven generations remain usable after proofing-authority rotation or withdrawal; a matching effective signed proof revocation is required to invalidate their proof before expiry. This boundary proves only that an operator-authorized external authority signed the exact identity and key-custody binding DataGround accepted, and that the proof remained within its declared validity and outside the recorded effective revocation set when preparation, transport reservation, or acknowledgement committed. Selecting external proofing and revocation authorities, distributing reviewed profiles, defining and retaining underlying evidence, acquiring and delivering external notices, independent clock policy, authenticated production transport, recipient decryption and access, retention, legal hold, and deletion remain deployment responsibilities.
