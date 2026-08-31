# Deterministic reference runtime

The reference runtime proves DataGround's native agent-service contract without OpenShell,
provider credentials, or a native harness. It has two modes. Process-local mode is intentionally
in-memory and loopback-only. Durable mode stores commands and event journals in PostgreSQL and
executes them through a replaceable worker with persistent reference-provider receipts. Both reference modes require the explicit loopback-only development bearer identity and static Cedar action policy described below. Durable mode withholds each completed authentication outcome and action evaluation until its append-only PostgreSQL audit row commits, using one correlation across both boundaries; process-local mode has no durable audit. Durable mode can export bounded frozen authorization-decision pages through an audited internal operator command, seal exact pages through an external signing boundary, encrypt each envelope to an identity-proven X25519 recipient, verify the exact package at one bound immutable mTLS S3 object under a short-lived signed workload identity bound to the exact client certificate, acquire either signed revocation-notice type through registered authenticated HTTPS, and record recipient-signed acknowledgement evidence under that same active isolation-scoped trust generation without a matching effective signed revocation. A separate opt-in loopback profile composes the provider-neutral OIDC/DPoP assembly, pinned discovery import, atomic keyset publication, sequential admission-policy rollout, deployment-selected PostgreSQL nonce enforcement, exact capacity-evidence incorporation, and bounded refresh ownership without changing either reference mode. A dedicated-database runner measures the exact PostgreSQL admission and nonce implementations, while startup and readiness bind one accepted record to the serving source, runtime, policy, deployment, and database profile. The OIDC API now requires an external-key signed certification for the exact clean build and reviewed files before database access. That release certificate incorporates a separate short-lived provider-neutral external-key DPoP issuance envelope and trust profile, cross-binds their provider, registry, issuer, and audience to the OIDC configuration, and removes non-liveness readiness at the earlier evidence expiry without enabling public traffic. The durable API can also opt into one exact governed-development dispatch target; this changes only durable invocation admission and leaves both reference modes unchanged when omitted. External provider conformance execution and monitoring, reviewed TLS ingress, complete production release certification beyond that slice, production policy distribution or entity loading, external audit-transport identity and recipient-proof issuance, acquisition-credential issuance and remote revocation, complete external evidence monitoring, audit-transport production certification, access or retention policy, default governed execution, and artifact content storage remain unresolved.

## Implemented lifecycle

Every resource path begins with an isolation-domain identifier, every request is bound to an authenticated development principal permitted for that exact domain, and every mutation requires an
`Idempotency-Key` header. A caller can:

1. create and list agent services in one exact isolation domain;
2. create, list, and publish immutable revisions using runtime profile `reference/v1`;
3. read and assign an exact stable alias with optimistic version checks;
4. invoke that alias and retrieve the normalized invocation result;
5. replay typed events with SSE and resume after `Last-Event-ID`;
6. cancel a waiting invocation; and
7. retrieve governed artifact metadata.

Service and revision discovery return bounded newest-first pages. Each opaque continuation cursor
binds the next page to the last observed service creation or revision-number boundary and never
substitutes for isolation-domain authorization. Revision discovery also requires the exact service
resource to exist in that domain. Process-local pages disappear on restart; PostgreSQL-backed pages
remain durable.

Exact alias discovery distinguishes a missing alias from a missing parent service. It returns the
current alias generation and version even when that alias targets an older published revision, so
clients can recover the required optimistic precondition before changing the route.

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
