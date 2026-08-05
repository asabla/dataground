# Authorization audit export

Durable deployments can export completed API-entry and invocation-effect authorization decisions with the internal `dataground-audit-export` operator command. The command is scoped to one isolation domain and reads both append-only decision tables through a repeatable-read PostgreSQL snapshot. Database credentials are the administrative authorization boundary; this is not a public API.

Start a frozen export with a new stable export identifier and an empty cursor:

```shell
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export \
  -export-id aex_00000000000000000001 \
  -isolation-domain iso_00000000000000000001 \
  -actor operator@example.invalid \
  -reason 'security review' \
  -correlation-id cor_00000000000000000001 \
  -limit 500
```

The command writes one `dataground.dev.authorization-audit-export/v1` JSON document to standard output. Its `content` contains the attributed export page and `contentSha256` binds the exact compact content encoding retained by the export receipt. Pass `content.nextCursor` as `-cursor` with a new export ID and correlation ID to read the next page. `content.complete: true` means that the frozen snapshot has no further records. A new empty-cursor run captures a new snapshot and can include decisions committed after the earlier run began.

Every page requires a stable export ID, operator identity, reason, and correlation ID. PostgreSQL records an append-only receipt containing the scope, attribution, reason digest, request digest, frozen cursor, record count, and content digest. It never stores the free-text reason. Exact replay under the same export ID returns the same document; changed attribution, scope, cursor, limit, or reason under that ID fails as a conflict. The opaque cursor is versioned, bounds API and invocation sequences independently, and must be stored without modification. It contains no credential or policy content.

The canonical output schema is `contracts/schemas/authorization-audit-export.schema.json`. API records contain the validated principal, action, resource, outcome, correlation, and immutable policy identity. Invocation records contain the durable actor, operation, invocation, service, revision, phase, outcome, correlation, and immutable policy identity. Sequences are decimal strings so their full PostgreSQL 64-bit precision survives JSON consumers.

The export excludes bearer tokens, prompts, request bodies, schemas, policy bytes, Cedar diagnostics, runtime inputs and outputs, artifacts, provider data, and free-text operator reasons. The separate [audit export signing](audit-export-signing.md) command can authenticate the exact canonical page with an external Ed25519 key, [audit export encryption](audit-export-encryption.md) can protect that envelope for an identity-proven X25519 recipient, and the [delivery lifecycle](audit-export-delivery.md) can bind the exact encrypted package to a versioned destination, verify it at one immutable loopback or pinned-mTLS S3 object, and require a recipient-signed receipt. The database receipt is evidence of local extraction, the envelope signature proves that the export signing key approved those exact bytes, and an acknowledged version 5 delivery proves the configured object and authorized recipient keys approved the same encrypted package and delivery binding. It does not prove workload-identity issuance, production transport certification, continued recipient retention, or plaintext erasure. Operators must protect plaintext export and envelope files and separately govern certificate issuance and revocation, server and bucket authorization, production certification, proofing and revocation authorities, external notice acquisition, access, retention, legal hold, and deletion. Authorization decisions and export receipts remain append-only and unchanged by later exports.
