# Operator audit export

Durable deployments can export append-only operator and lifecycle audit records with the internal `dataground-operator-audit-export` command. The command reads one isolation domain through a frozen PostgreSQL sequence boundary and withholds the page until an immutable receipt binds the exact request and compact content digest. Database credentials are the administrative authorization boundary; this is not a public API.

Run the command against a current migrated database:

```sh
DATAGROUND_DATABASE_URL='postgres://dataground:dataground@127.0.0.1:5432/dataground?sslmode=disable' \
go run ./cmd/dataground-operator-audit-export \
  -export-id oax_00000000000000000001 \
  -isolation-domain iso_00000000000000000001 \
  -actor operator@example.invalid \
  -reason 'incident evidence export' \
  -correlation-id cor_00000000000000000001 \
  -limit 500
```

The command writes one `dataground.dev.operator-audit-export/v1` JSON document to standard output. Its `content` contains the attributed page and `contentSha256` binds the exact compact content encoding retained by the receipt. Pass `content.nextCursor` as `-cursor` with a new export ID and correlation ID to continue the frozen snapshot. `content.complete: true` means no further records existed at the captured boundary. A new empty-cursor export captures a new boundary and may include later records.

The canonical schema is `contracts/schemas/operator-audit-export.schema.json`. Each record contains its stable audit identifier, export sequence, occurrence time, actor, action, resource identity, coarse outcome, correlation, optional operation identity, and a closed allowlist of safe scalar metadata. The sequence is a PostgreSQL export order and may contain gaps; `recordedAt` retains event time. Metadata permits only the reviewed digest, identity, provider-binding, generation, endpoint, artifact, policy, and size fields emitted by current writers. Adding another metadata key requires an intentional contract change.

The export excludes credentials and credential digests, provider responses, tokens, prompts, request bodies, policy bytes, artifact contents, object routes, free-text operator reasons, and dependency diagnostics. It does not sign, encrypt, upload, acknowledge, retain, or delete records. The database receipt is evidence of local extraction, not external delivery or authenticity. Operators must protect standard output and separately define transport, acknowledgement, access, retention, legal hold, and deletion policy. Audit records and export receipts remain append-only and unchanged by later exports.
