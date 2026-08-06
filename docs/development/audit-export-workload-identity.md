# Audit export workload identity

The internal `dataground-audit-export-workload-identity` command authorizes which deployment-issued mTLS client certificate may execute a new audit export transport. DataGround does not mint certificates or select an issuer. A deployment-owned workload identity authority instead signs a short-lived grant for one isolation domain, workload identifier, exact audience, and exact client-certificate file digest. DataGround verifies that evidence against a pinned public trust profile and records sequential activation, rotation, or local revocation events in PostgreSQL.

The issuer trust profile contract is `dataground.audit-export-workload-identity-trust/ed25519/v1`. It contains one deployment-owned authority identifier and one to eight sorted Ed25519 verification keys. The signed grant contract is `dataground.audit-export-workload-identity-grant/ed25519/v1`; its content binds `dataground.audit-export-workload-identity-grant/v1`, the isolation domain, workload identifier, fixed `dataground.audit-export-transport` audience, SHA-256 digest of the complete client-certificate chain file, issuer identifier, issue time, not-before time, and expiry. Schemas and checked fixtures are under `contracts/schemas` and `contracts/fixtures`.

The grant `contentSha256` is SHA-256 over the compact canonical `content` object. The signing input is the ASCII domain `DataGround audit export workload identity grant v1`, a newline, then one compact JSON object with fields in this order: `contract`, `content`, `contentSha256`, `issuerTrustProfileSha256`, and `keyId`, followed by one newline. The signature is raw Ed25519 encoded as unpadded base64url.

Activation requires duplicate-free one-line canonical JSON files with one final newline, mode `0600`, beneath owner-only directories. The command checks the grant signature, exact trust-profile digest, audit-export audience, domain, workload, certificate digest, canonical microsecond timestamps, five-minute future issue tolerance, and current validity before opening PostgreSQL. The database independently uses its own clock and requires the grant to be active before inserting the event:

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-workload-identity \
  -operation activate \
  -isolation-domain iso_00000000000000000001 \
  -workload audit-export.dispatcher \
  -generation 1 \
  -client-certificate-sha256 sha256:REPLACE_WITH_COMPLETE_CERTIFICATE_FILE_DIGEST \
  -grant-file /run/dataground/audit/workload-grant.json \
  -issuer-trust-file /run/dataground/audit/workload-issuer-trust.json \
  -actor operator@example.invalid \
  -reason 'authorize the reviewed audit export dispatcher identity' \
  -correlation-id cor_00000000000000000001
```

Rotation is the next sequential activation with a different signed grant. It may overlap certificate validity at the issuer, but DataGround authorizes only the latest local generation for a new transport reservation. Local revocation is the next generation and must name the exact active grant and certificate digests; it appends a tombstone rather than deleting evidence:

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-workload-identity \
  -operation revoke \
  -isolation-domain iso_00000000000000000001 \
  -workload audit-export.dispatcher \
  -generation 2 \
  -grant-sha256 sha256:REPLACE_WITH_ACTIVE_GRANT_DIGEST \
  -client-certificate-sha256 sha256:REPLACE_WITH_ACTIVE_CERTIFICATE_FILE_DIGEST \
  -actor operator@example.invalid \
  -reason 'withdraw the audit export dispatcher credential locally' \
  -correlation-id cor_00000000000000000002
```

Version 6 delivery preparation binds a version 3 destination whose digest covers the workload identifier, exact grant digest, local generation, certificate digest, and pinned server trust. Transport reservation serializes against workload identity changes and externally signed issuer revocation notices, and rejects an inactive, rotated, locally revoked, externally revoked, not-yet-valid, expired, wrong-audience, cross-domain, or substituted grant independently in Go and PostgreSQL. Exact reserved replay reauthorizes the current generation and external revocation state before repeating the object effect. Completion does not reauthorize because the immutable object may already exist after an ambiguous database outcome; it records the exact identity evidence reserved before the effect.

Grant activation, local revocation, and external issuer-revocation intake audit credential identifiers and digests without storing certificate bytes, private keys, signed evidence bytes, or authority public keys. External certificate and grant issuance, private-key delivery, revocation-notice acquisition, authority governance, server authorization and bucket policy, DNS governance, production topology, and production release certification remain deployment and release boundaries. See [audit export workload identity revocation](audit-export-workload-identity-revocation.md) for the external notice contract and enforcement boundary.
