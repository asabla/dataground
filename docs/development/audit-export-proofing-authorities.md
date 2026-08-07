# Audit export proofing authorities

The internal `dataground-audit-export-proofing-authority` command governs which external Ed25519 proofing profile may authorize a new audit-export recipient trust activation. Each activation is bound to one isolation domain, authority identifier, exact canonical trust-profile digest, sorted signing-key set, and sequential PostgreSQL generation. After schema migration, historical recipient trust generations remain usable under their recorded proof expiry and revocation rules, but a new activation fails closed until an operator activates the exact proofing profile and signer.

The command's database credential is the administrative authorization boundary; this is not a public trust-management API. Activate a reviewed profile before accepting recipient identity and key-custody proofs signed by it:

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-proofing-authority \
  -operation activate \
  -isolation-domain iso_00000000000000000001 \
  -authority archive-proofing.primary \
  -generation 1 \
  -trust-file /run/dataground/audit/archive-proofing-trust.json \
  -actor operator@example.invalid \
  -reason 'authorize the reviewed recipient proofing authority' \
  -correlation-id cor_00000000000000000001
```

Activation requires a canonical `dataground.audit-export-recipient-proofing-trust/ed25519/v1` file beneath an owner-only directory. The command re-hashes the complete profile, records only its contract, digest, authority identifier and key identifiers, and does not persist public or private keys.

Rotation is the next sequential activation with a different authority identifier or trust-profile digest. Only the latest generation can authorize a new recipient trust activation; older generations remain immutable evidence and exact historical replay remains read-only. Withdraw the exact active profile by appending the next generation without supplying the trust file:

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-proofing-authority \
  -operation revoke \
  -isolation-domain iso_00000000000000000001 \
  -authority archive-proofing.primary \
  -generation 2 \
  -trust-sha256 sha256:REPLACE_WITH_ACTIVE_PROFILE_DIGEST \
  -actor operator@example.invalid \
  -reason 'withdraw the compromised recipient proofing profile' \
  -correlation-id cor_00000000000000000002
```

Authority changes, proof-revocation intake, and recipient activation serialize under the same isolation-scoped PostgreSQL lock. Go and database-trigger checks require the latest active authority, profile and signing key before a new activation can commit. A generation's complete key set must be installed in the same transaction as its activation. Authority events, key identifiers and operator audits are append-only; changed replay, skipped generations, cross-domain use, profile substitution, unlisted or late-added signing keys, direct-SQL bypass, mutation and deletion fail closed. Rotation or withdrawal does not retroactively invalidate an already accepted recipient trust generation; externally established compromise is represented by the separately governed signed proof-revocation path.

This boundary does not select the external authority, distribute reviewed trust profiles, define proofing methods or assurance levels, create or acquire identity evidence, retain the underlying evidence, or certify production transport. Those remain deployment and release responsibilities.
