# Deterministic reference runtime

The reference runtime proves DataGround's native agent-service contract without OpenShell,
provider credentials, or a native harness. It has two modes. Process-local mode is intentionally
in-memory and loopback-only. Durable mode stores commands and event journals in PostgreSQL and
executes them through a replaceable worker with persistent reference-provider receipts. Both modes require the explicit loopback-only development bearer identity and static Cedar action policy described below. Durable mode withholds each completed authentication outcome and action evaluation until its append-only PostgreSQL audit row commits, using one correlation across both boundaries; process-local mode has no durable audit. Durable mode can export bounded frozen authorization-decision pages through an audited internal operator command. The internal provider-neutral OIDC boundary can verify compact JWT access tokens against an immutable deployment-owned JWKS profile, atomically replace complete versioned keyset snapshots, and bind issuer/subject/audience identity through audited, append-only PostgreSQL domain registrations and revocations, but it is not composed into either executable mode. Neither mode implements OIDC discovery, deployment keyset publication or refresh scheduling, replay-resistant transport, production policy distribution or entity loading, audit delivery, acknowledgement, access or retention policy, a real harness, or artifact content storage.

## Implemented lifecycle

Every resource path begins with an isolation-domain identifier, every request is bound to an authenticated development principal permitted for that exact domain, and every mutation requires an
`Idempotency-Key` header. A caller can:

1. create an agent service;
2. create and publish an immutable revision using runtime profile `reference/v1`;
3. assign a stable alias with optimistic version checks;
4. invoke that alias and retrieve the normalized invocation result;
5. replay typed events with SSE and resume after `Last-Event-ID`;
6. cancel a waiting invocation; and
7. retrieve governed artifact metadata.

The canonical routes, request bodies, responses, errors, and examples live in
[`contracts/openapi/dataground-api.openapi.json`](../../contracts/openapi/dataground-api.openapi.json)
and [`contracts/fixtures`](../../contracts/fixtures). The API never returns a gateway address,
sandbox identity, provider endpoint, or credential.

## Deterministic scenarios

Set `input.scenario` on invocation to one of:

| Scenario | Contract behavior |
| --- | --- |
| `success` | text, tool, process, usage, and successful completion |
| `question` | question event followed by waiting state |
| `approval` | approval request followed by waiting state |
| `artifact` | large sensitive output represented only by a governed artifact descriptor |
| `cancellation` | runtime-originated cancellation |
| `retryable_failure` | safe retryable error and failed lifecycle |
| `terminal_failure` | safe non-retryable error and failed lifecycle |
| `duplicate` | identical duplicate delivery normalized to one journal record |
| `out_of_order` | out-of-order delivery normalized to monotonic sequence |
| `unknown_optional` | unknown event type and namespaced extension preserved safely |

An unregistered scenario fails validation. Publishing a revision with a runtime profile other than
`reference/v1`, or with a required capability the reference manifest does not support, fails
closed.

## Compatibility and recovery boundary

SSE `id` values are invocation-local journal sequences. Reconnecting with `Last-Event-ID` replays
only later records with stable event identities. Identical mutation retries return the first
response; reuse of the same key with a different body returns `IDEMPOTENCY_KEY_REUSED`.

Process-local mode is not restart-safe and must not be presented as the durable control plane.
Durable mode uses explicit publication and invocation tables, durable `due_at` timers, expiring
leases with monotonically increasing fencing tokens, transactional outbox and audit writes, and
deterministic external effect IDs. A replacement worker observes the persistent provider receipt
before repeating an ambiguous effect. Operational limits and recovery procedures are documented
in [durable control-plane operations](durable-control-plane.md).
