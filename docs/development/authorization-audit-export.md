# Authorization audit export

Durable deployments can export completed API-entry and invocation-effect authorization decisions with the internal `dataground-audit-export` operator command. The command is scoped to one isolation domain and reads both append-only decision tables through one repeatable-read PostgreSQL transaction. Database credentials are the administrative authorization boundary; this is not a public API.

Start a frozen export with an empty cursor:

```shell
DATAGROUND_DATABASE_URL='postgres://dataground:dataground@127.0.0.1:5432/dataground?sslmode=disable' \
go run ./cmd/dataground-audit-export \
  -isolation-domain iso_00000000000000000001 \
  -limit 500
```

The command writes one `dataground.dev.authorization-audit-export/v1` JSON page to standard output. Pass its exact `nextCursor` value as `-cursor` to read the next page. `complete: true` means that the frozen snapshot has no further records. A new empty-cursor run captures a new snapshot and can include decisions committed after the earlier run began. The opaque cursor is versioned, bounds API and invocation sequences independently, and must be stored without modification. It contains no credential or policy content.

The canonical output schema is `contracts/schemas/authorization-audit-export.schema.json`. API records contain the validated principal, action, resource, outcome, correlation, and immutable policy identity. Invocation records contain the durable actor, operation, invocation, service, revision, phase, outcome, correlation, and immutable policy identity. Sequences are decimal strings so their full PostgreSQL 64-bit precision survives JSON consumers.

The export excludes bearer tokens, prompts, request bodies, schemas, policy bytes, Cedar diagnostics, runtime inputs and outputs, artifacts, provider data, and free-text operator reasons. It does not sign, encrypt, upload, acknowledge, retain, or delete records. Operators must route standard output into an appropriately protected sink and manage delivery, access, retention, legal hold, and deletion under a separately reviewed deployment policy. Database rows remain append-only and unchanged by export.
