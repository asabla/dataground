# Audit export recipient proof revocation

The internal `dataground-audit-export-recipient-proof-revocation` command verifies and records a canonical external revocation notice before PostgreSQL can use the affected recipient identity proof for a new trust activation or delivery acknowledgement. A separate deployment-owned revocation authority signs the notice, so revocation does not depend on a proofing key that may itself be compromised.

The notice contract is `dataground.audit-export-recipient-proof-revocation/ed25519/v1`. It binds the isolation domain, proofing authority, exact proofing trust profile, profile- or key-level scope, opaque external reason digest, revocation authority, issue time, and effective time. Its revocation trust profile uses `dataground.audit-export-recipient-revocation-trust/ed25519/v1`. Schemas and fixtures are under `contracts/schemas` and `contracts/fixtures`.

The command requires canonical owner-only files and parent directories, re-hashes the notice and trust profile, verifies the Ed25519 signature, requires distinct proofing and revocation authority identifiers, rejects a notice issued more than five minutes ahead of the command clock, and validates the explicit isolation-domain binding before opening PostgreSQL:

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-recipient-proof-revocation \
  -isolation-domain iso_00000000000000000001 \
  -revocation-file /run/dataground/audit/archive-proof-revocation.json \
  -revocation-trust-file /run/dataground/audit/archive-revocation-trust.json \
  -actor operator@example.invalid \
  -reason 'record the externally authorized proofing-key revocation' \
  -correlation-id cor_00000000000000000001
```

The notice `contentSha256` is SHA-256 over the compact canonical `content` object. Its signing input is the ASCII domain `DataGround audit export recipient proof revocation v1`, a newline, and one compact JSON object containing `contract`, `content`, `contentSha256`, `revocationTrustProfileSha256`, and `keyId` in that order, followed by one newline.

Profile scope revokes every identity proof bound to the exact proofing authority and proofing trust profile. Key scope narrows that match to one proofing signing key. The effective time may precede issue time to represent a retrospectively established compromise, or follow it for a scheduled withdrawal. PostgreSQL uses its own clock: future-effective notices remain recorded but do not block until effective. Notices cannot be withdrawn or weakened; recovery requires a newly proofed recipient activation outside the revoked profile or key scope.

Revocation evidence is append-only and exact replay is read-only. Reusing a notice digest with changed attribution or a correlation identifier for another notice fails closed. The database stores minimized notice provenance, authority and subject identifiers, timestamps, operator attribution, external and operator reason digests, and the complete notice digest. It does not store public or private keys, notice bytes, external incident details, recipient keys, or transport credentials.

Revocation recording, recipient activation, and acknowledgement acquire one isolation-domain revocation lock before the recipient-specific trust lock. An effective notice therefore commits before or after a consequential transition, never between its authorization check and durable write. New activations under a revoked proofing profile or signer fail, and pending delivery acknowledgements under an affected active generation fail. Completed acknowledgements and historical trust generations remain immutable evidence and exact completed replay remains read-only.

This boundary proves only that DataGround verified and durably enforced a notice under the current isolation-scoped [revocation authority generation](audit-export-revocation-authorities.md). The separate [revocation importer](audit-export-revocation-acquisition.md) can retrieve the notice and matching active profile from authenticated HTTPS endpoints only under the current sequential source generation. Selecting the external authority and source, publishing the registry before activation, issuing and remotely revoking endpoint credentials, monitoring compromise channels, retaining external incident evidence, and deciding whether completed exports require an out-of-band response remain deployment responsibilities. Transport execution, encryption, recipient access, retention, legal hold, and deletion also remain outside this command.
