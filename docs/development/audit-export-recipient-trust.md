# Audit export recipient trust

The internal `dataground-audit-export-recipient-trust` command authorizes one exact recipient trust profile for one isolation domain and deployment-owned recipient only after verifying an external proofing authority's signed identity and key-custody statement. PostgreSQL retains append-only sequential activation and revocation generations, the complete profile digest, its sorted key identifiers, minimized proof provenance and validity, operator attribution, and a reason digest. It does not store public-key bytes, private keys, proof or receipt bytes, destination material, or transport credentials.

Activation requires the canonical owner-only recipient trust profile described in [audit export delivery receipts](audit-export-delivery-receipts.md), one canonical signed identity proof, and the exact owner-only proofing trust profile that authorizes its signer. The proof binds the isolation domain, recipient, recipient trust profile digest, proofing authority, an opaque SHA-256 digest of the authority's external evidence, and a UTC validity interval. The command validates and re-hashes all three files before opening PostgreSQL:

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
  -trust-sha256 sha256:REPLACE_WITH_ACTIVE_PROFILE_DIGEST \
  -actor operator@example.invalid \
  -reason 'revoke the archive receipt keys after custody change' \
  -correlation-id cor_00000000000000000002
```

Generations are gap-free per isolation domain and recipient. The first event must be generation 1 activation; revocation must match the active digest; a later activation may reauthorize the same digest only after revocation. One legacy version 1 activation may be upgraded in place conceptually by appending a version 2 activation for the same profile with valid identity proof; no new version 1 event is accepted. Exact replay is read-only, while changed attribution or proof provenance, correlation reuse, gaps, rollback, cross-domain reuse, unsorted or duplicate keys, and mutation or deletion of either event or key rows fail closed.

For a new version 3 delivery, acknowledgement and trust resolution share one PostgreSQL transaction and advisory scope. The transition requires the receipt's exact profile digest and signing key to belong to the latest identity-proven active generation and requires that generation's proof to remain unexpired according to the PostgreSQL clock. Concurrent activation, rotation, or revocation therefore completes before or after acknowledgement, never between trust evaluation and the durable transition. Later expiry or revocation does not rewrite a completed acknowledgement, and exact acknowledgement replay remains read-only. Completed version 2 deliveries retain their version 1 receipt evidence without gaining an authorization or identity-proofing claim; only pending deliveries upgrade to version 3.

This boundary proves only that a configured external authority signed the exact identity and key-custody binding DataGround accepted, and that the proof remained within its declared validity when a delivery acknowledgement committed. Selecting the authority, defining and retaining its underlying proofing evidence, independent clock policy, external compromise notification or revocation, transport execution, encryption, recipient access, retention, legal hold, and deletion remain deployment responsibilities.
