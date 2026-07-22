# Durable control-plane operations

## Scope and invariants

The initial durable slice owns two explicit finite state machines: service publication and invocation execution. PostgreSQL is authoritative for desired and observed state, state-machine version, generation, attempt, durable due time and deadline, lease owner and fencing token, error classification, terminal result, command idempotency, external-effect observation, event replay, outbox delivery state, and audit correlation.

There is no generic workflow table or user-authored workflow language. The worker's polling ticker is only a wake-up mechanism; `due_at` remains the durable timer. Every claimed operation is selected fairly from the oldest due operation per isolation domain. A transition can commit only while the worker still owns the matching lease owner and fencing token.

The reference runtime stores provider-side receipts in `reference_runtime_receipts`. Invocation state-machine version 2 records a distinct `start-invocation` effect before the `run-invocation` effect, so successful sandbox admission cannot be mistaken for completed runtime work. Existing version 1 operations retain their original single-effect reconciliation and can finish without reinterpretation; only newly accepted invocations use version 2. An optional version 2 start-effect driver resolves the invocation's persisted service, revision, principal and correlation scope, observes any execution already created for the operation, and requires explicit consequential-effect authorization immediately before new admission. A durable denial or structurally invalid target records a bounded terminal failure instead of retrying; policy-service, storage, and provider availability failures retain the bounded retry path. The default worker routes both phases to the deterministic reference driver. Separately, the OpenShell adapter can persist protected gateway, placement, sandbox-routing, and observed-state records through its PostgreSQL state store; it is not a configured public execution path or a real harness certification.

## Migration policy

Run migrations as a separate deployment step before starting a new API or worker:

```shell
DATAGROUND_DATABASE_URL="$DATABASE_URL" go run ./cmd/dataground-migrate up
DATAGROUND_DATABASE_URL="$DATABASE_URL" go run ./cmd/dataground-migrate check
```

The migration runner takes a transaction-scoped PostgreSQL advisory lock, applies each embedded migration atomically, and records its version. API and worker processes require the exact current schema version and fail closed on an older or newer version. This deliberately disallows a mixed-schema fleet until a future migration explicitly declares a tested compatibility window.

For the current pre-production schema, the tested rollback is:

```shell
DATAGROUND_DATABASE_URL="$DATABASE_URL" go run ./cmd/dataground-migrate down-to-0
```

`down-to-0` deletes all control-plane data and is only for disposable development databases. Production migrations must add backup, expand/contract, and data-preserving rollback evidence before this command can be certified there. The CI migration test runs up, down, up, and exact-version validation against PostgreSQL 18.4.

## Deployment and recovery

1. Stop new command admission or keep the old API fleet running.
2. Back up PostgreSQL using the installation's verified backup procedure.
3. Run `dataground-migrate up`, followed by `check`.
4. Start workers, then API instances. Readiness requires a live database connection.
5. Confirm that due operations are claimed and that stale leases expire naturally.

Killing an API after command commit cannot lose the command because the resource change, idempotency result, outbox event, and audit record share one transaction. Killing a worker after an external provider acknowledgement may leave the effect status unknown; the next worker observes the deterministic effect ID before applying it again. Expired or replaced leases cannot commit because every transition is fenced.

The same worker process runs a separately bounded outbox dispatcher. It fairly claims one event per isolation domain, publishes through a replaceable publisher interface, and uses a fenced lease before marking delivery. The current reference publisher acknowledges locally for conformance testing; durable signed webhook transport remains a later publisher implementation.

## Repair

Only a failed operation is repairable. The operator must supply an authenticated actor identity from the surrounding administrative channel, a reason, and a stable deduplication ID:

```shell
DATAGROUND_DATABASE_URL="$DATABASE_URL" go run ./cmd/dataground-repair \
  -kind invocation-execution \
  -isolation-domain iso_example00000000000000 \
  -operation op_example000000000000000 \
  -actor operator@example.invalid \
  -reason 'provider recovered after incident' \
  -deduplication-id incident-123-repair-1 \
  -deadline 30m
```

The new deadline is required because the failed operation's original deadline may already have expired. Repair preserves the original request actor and correlation while recording the authenticated repair actor and deduplication correlation as the principal for subsequent effect authorization, transition audit, and invocation admission. The command is repeatable: its actor-bound deduplication record, operation transition, outbox event, and audit record commit atomically. Reusing the deduplication ID with different content, a different actor, or a different deadline is rejected. The reason contributes to the immutable command digest; the audit record carries the actor, operation, outcome, and deduplication correlation without copying free text into telemetry. It does not repair cancelled or successful operations. Until platform authentication exists, access to this executable and its database credential is the authorization boundary; do not expose it through the public API.

## Safe telemetry

Reconciler spans contain operation kind, stable operation ID, isolation-domain ID, and attempt number. Outbox and audit payloads contain resource identity, outcome, actor, operation, and correlation identifiers. Invocation inputs, outputs, event payloads, provider responses, credentials, and raw errors are not exported through telemetry or audit metadata.

## Known limits

- Public durable mode still uses the deterministic reference driver. The governed invocation start-effect driver is available for explicit composition, including version 2 repairs with a persisted repair principal, but no default worker configures an authorizer, object transport, gateway, or private policy workspace.
- Outbox rows are written atomically but external webhook delivery is a later bounded state machine.
- Authentication, Cedar authorization, provider credential brokering, object storage, and artifact content delivery are not implemented.
- The exact-schema startup rule means rolling mixed-version upgrades are intentionally unavailable until an expand/contract migration is tested.
