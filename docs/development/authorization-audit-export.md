# Authorization audit export

Durable deployments can export completed API-entry and invocation-effect authorization decisions with the internal `dataground-audit-export` operator command. The command is scoped to one isolation domain. Its database credential and surrounding administrative channel are the authorization boundary; this is not a public API.

Start a frozen export with an empty cursor and a stable export identity:

```shell
DATAGROUND_DATABASE_URL='postgres://dataground:dataground@127.0.0.1:5432/dataground?sslmode=disable' \
go run ./cmd/dataground-audit-export \
  -export-id aex_00000000000000000001 \
  -isolation-domain iso_00000000000000000001 \
  -actor operator@example.invalid \
  -reason 'incident evidence export' \
  -correlation-id cor_00000000000000000001 \
  -limit 500
```

The command writes one `dataground.dev.authorization-audit-export/v1` JSON document to standard output. Its `content` object carries the exact export, operator, scope, correlation, cursor, records, and completion state; `contentSha256` binds the canonical compact JSON encoding of that object. Pass the exact `content.nextCursor` value as `-cursor` with a new export ID and correlation ID to read the next page. `content.complete: true` means the frozen snapshot has no further records.

The first page captures independent high-water marks for the API and invocation decision identities. Pages preserve API sequence order followed by invocation sequence order; `recordedAt` remains evidence and is not presented as a global ordering guarantee across the two streams. The opaque cursor is versioned, bounds both streams independently, and contains no credential or policy content. A new empty-cursor run captures a new snapshot and may include decisions committed after an earlier run began.

Every page requires a stable `export-id`. Before document bytes are released, PostgreSQL stores an append-only receipt binding the exact request digest, actor, reason digest, correlation, frozen cursor, limit, record count, and content digest. An identical retry reconstructs and verifies the same page. Reusing the export ID with changed attribution, scope, cursor, reason, correlation, or limit fails, and a failed or uncertain receipt write releases no evidence.

The canonical output schema is `contracts/schemas/authorization-audit-export.schema.json`. API records contain the validated principal, action, resource, outcome, correlation, and immutable policy identity. Invocation records contain the durable actor, operation, invocation, service, revision, phase, outcome, correlation, and immutable policy identity. Neither shape can carry tokens, policy bytes, Cedar diagnostics, request bodies, prompts, schemas, artifact content, provider data, or runtime output.

A page contains at most 1,000 records. Export transport, delivery acknowledgement, retention schedules, legal hold, deletion policy, encryption, and production access control remain deployment and compliance boundaries; the command does not claim to implement them.
