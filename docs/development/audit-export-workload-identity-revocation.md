# Audit export workload identity revocation

The internal `dataground-audit-export-workload-identity-revocation` command verifies and records a canonical external revocation notice before PostgreSQL can authorize an affected workload grant for activation or a new transport reservation. A separate deployment-owned revocation authority signs the notice, so revocation does not depend on an issuer key that may itself be compromised.

The notice contract is `dataground.audit-export-workload-identity-revocation/ed25519/v1`. It binds the isolation domain, workload identity authority, exact issuer trust profile, profile- or key-level scope, opaque external reason digest, revocation authority, issue time, and effective time. Its trust profile uses `dataground.audit-export-workload-identity-revocation-trust/ed25519/v1`. Schemas and fixtures are under `contracts/schemas` and `contracts/fixtures`.

The command requires canonical owner-only files and parent directories, re-hashes the notice and trust profile, verifies the Ed25519 signature, requires distinct workload-identity and revocation authority identifiers, rejects a notice issued more than five minutes ahead of the command clock, and validates the exact isolation-domain binding before opening PostgreSQL:

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-workload-identity-revocation \
  -isolation-domain iso_00000000000000000001 \
  -revocation-file /run/dataground/audit/workload-issuer-revocation.json \
  -revocation-trust-file /run/dataground/audit/workload-revocation-trust.json \
  -actor operator@example.invalid \
  -reason 'record the externally authorized workload issuer-key revocation' \
  -correlation-id cor_00000000000000000001
```

The notice `contentSha256` is SHA-256 over the compact canonical `content` object. Its signing input is the ASCII domain `DataGround audit export workload identity revocation v1`, a newline, and one compact JSON object containing `contract`, `content`, `contentSha256`, `revocationTrustProfileSha256`, and `keyId` in that order, followed by one newline.

Profile scope revokes every grant bound to the exact workload identity authority and issuer trust profile. Key scope narrows that match to one issuer signing key. The effective time may precede issue time to represent a retrospectively established compromise, or follow it for a scheduled withdrawal. PostgreSQL uses its own clock: future-effective notices remain recorded but do not block until effective. Notices cannot be withdrawn or weakened; recovery requires a newly issued grant under an unaffected profile or key.

Revocation evidence is append-only and exact replay is read-only. Reusing a notice digest with changed attribution or a correlation identifier for another notice fails closed. The database stores minimized notice provenance, authority and subject identifiers, timestamps, operator attribution, external and operator reason digests, and the complete notice digest. It does not store public or private keys, notice bytes, external incident details, certificates, or transport credentials.

Revocation recording, workload grant activation, and transport reservation acquire one isolation-domain revocation lock before the workload-specific identity lock. An effective notice therefore commits before or after a consequential transition, never between its authorization check and durable write. New activations and reservations under a revoked issuer profile or signer fail. Existing reserved transports may still record completion because the immutable object effect may already have occurred, and historical delivery evidence remains unchanged.

This boundary proves only that DataGround verified and durably enforced a notice under the current isolation-scoped [revocation authority generation](audit-export-revocation-authorities.md). Selecting the external authority, distributing reviewed profiles, acquiring and delivering notices, monitoring compromise channels, retaining external incident evidence, issuing certificates and grants, and deciding whether completed exports require an out-of-band response remain deployment responsibilities. Server authorization, bucket policy, production topology, and release certification remain outside this command.
