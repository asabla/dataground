# Audit export revocation authorities

The internal `dataground-audit-export-revocation-authority` command governs which external Ed25519 authority profile may authorize a new audit-export revocation notice. Recipient-proof and workload-identity notices use separate purposes, so activating a profile for one cannot authorize the other. Each activation is bound to one isolation domain, purpose, authority identifier, exact canonical trust-profile digest, sorted signing-key set, and sequential PostgreSQL generation.

After schema migration, previously recorded notices remain effective and replayable, but new notice intake fails closed until an operator activates the corresponding authority purpose. The command's database credential is the administrative authorization boundary; this is not a public trust-management API.

Activate a reviewed recipient-proof revocation profile before recording notices signed by it:

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-revocation-authority \
  -operation activate \
  -isolation-domain iso_00000000000000000001 \
  -purpose recipient-proof \
  -authority archive-revocation.primary \
  -generation 1 \
  -trust-file /run/dataground/audit/archive-revocation-trust.json \
  -actor operator@example.invalid \
  -reason 'authorize the reviewed recipient-proof revocation authority' \
  -correlation-id cor_00000000000000000001
```

Use `-purpose workload-identity` with a `dataground.audit-export-workload-identity-revocation-trust/ed25519/v1` profile for workload issuer notices. Activation requires a duplicate-free canonical one-line JSON file beneath an owner-only directory. The command re-hashes the complete profile, records only its contract, digest, authority identifier and key identifiers, and does not persist public or private keys.

Rotation is the next sequential activation with a different trust-profile digest. Only the latest generation can authorize a new notice; older generations remain immutable evidence. Withdraw the exact active profile by appending the next generation without supplying the trust file:

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-revocation-authority \
  -operation revoke \
  -isolation-domain iso_00000000000000000001 \
  -purpose recipient-proof \
  -authority archive-revocation.primary \
  -generation 2 \
  -trust-sha256 sha256:REPLACE_WITH_ACTIVE_PROFILE_DIGEST \
  -actor operator@example.invalid \
  -reason 'withdraw the compromised revocation authority profile' \
  -correlation-id cor_00000000000000000002
```

Authority changes and notice intake serialize under the same isolation-scoped PostgreSQL lock. Go and database-trigger checks require the latest active purpose, authority, profile and signing key before a new notice can commit. Each generation's complete key set must be installed in its activation transaction. Exact replay of an already-recorded notice remains read-only after rotation or withdrawal so historical evidence does not become unavailable. Authority events, key identifiers and operator audits are append-only; changed replay, skipped generations, cross-purpose use, cross-domain use, profile substitution, and unlisted or late-added signing keys fail closed.

This boundary does not decide which external authority should be trusted, monitor compromise channels, retain external incident evidence, or certify production transport. Those remain deployment and release responsibilities. The separate [revocation importer](audit-export-revocation-acquisition.md) can retrieve a notice and its exact active verification profile from registered authenticated HTTPS endpoints, but source selection, registry publication, endpoint credential issuance and remote revocation remain deployment-owned. Recipient [proofing-authority governance](audit-export-proofing-authorities.md) is separate; workload identity issuance, complete external monitoring, and production audit-transport certification remain unresolved.
