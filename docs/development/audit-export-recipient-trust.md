# Audit export recipient trust

The internal `dataground-audit-export-recipient-trust` command authorizes one exact recipient trust profile for one isolation domain and deployment-owned recipient. PostgreSQL retains append-only sequential activation and revocation generations, the complete profile digest, its sorted key identifiers, operator attribution, and a reason digest. It does not store public-key bytes, private keys, receipt bytes, destination material, or transport credentials.

Activation requires the canonical owner-only recipient trust profile described in [audit export delivery receipts](audit-export-delivery-receipts.md). The command validates and re-hashes that exact file before opening PostgreSQL:

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-recipient-trust \
  -operation activate \
  -isolation-domain iso_00000000000000000001 \
  -recipient archive.primary \
  -generation 1 \
  -trust-file /run/dataground/audit/archive-trust.json \
  -actor operator@example.invalid \
  -reason 'authorize the reviewed archive receipt keys' \
  -correlation-id cor_00000000000000000001
```

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

Generations are gap-free per isolation domain and recipient. The first event must be generation 1 activation; revocation must match the active digest; a later activation may reauthorize the same digest only after revocation. Exact replay is read-only, while changed attribution, correlation reuse, gaps, rollback, cross-domain reuse, unsorted or duplicate keys, and mutation or deletion of either event or key rows fail closed.

For a new version 3 delivery, acknowledgement and trust resolution share one PostgreSQL transaction and advisory scope. The transition requires the receipt's exact profile digest and signing key to belong to the latest active generation. Concurrent rotation or revocation therefore completes before or after acknowledgement, never between trust evaluation and the durable transition. Later revocation does not rewrite a completed acknowledgement, and exact acknowledgement replay remains read-only. Completed version 2 deliveries retain their version 1 receipt evidence without gaining an authorization claim; only pending deliveries upgrade to version 3.

This boundary authorizes DataGround's local acceptance of recipient signatures. Recipient identity proofing, public-key custody before activation, external compromise notification or revocation, transport execution, encryption, recipient access, retention, legal hold, deletion, and independent clock policy remain deployment responsibilities.
