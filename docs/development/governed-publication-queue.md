# Durable governed publication boundary

The internal repository supports a queued form of the [governed development publication](development-publication.md) transaction. This implements the durable publication boundary needed by ADR-003 and ADR-038. The worker command exposes this boundary through explicit database-authorized operator commands. Public publication remains reference-only and still requires a composition with the [publication authorization boundary](publication-authorization.md). `publish-development` retains its atomic operator behavior.

`Repository.QueueDevelopmentPublication` accepts an idempotent operator request containing one exact isolation domain, service, draft revision, expected revision version, plan digest, policy digest, and verification-configuration digest. The target must use `codex.app-server/v1` with that exact required capability. The request creates a version 3 publication operation and immutable reviewed-input row, with the acceptance audit and outbox event in the same transaction. It returns the existing operation representation with status 202. The draft does not become callable. Acceptance does not prove runtime readiness or grant publication authority to a public caller.

The idempotency digest binds the reviewed inputs and actor. An exact retry returns the original acceptance response, even after completion. A newly generated correlation or deadline does not replace the recorded values. Changed reviewed inputs conflict. The deadline must be current and within 24 hours. The original operation remains authoritative for current state; the stored acceptance response is historical.

Version 3 uses `queued → validating → published`. Failure and cancellation remain terminal alternatives, and explicit repair retains the original reviewed inputs. The reconciler requires a `GovernedPublicationDriver` before advancing this path. It never substitutes a reference publication receipt or a native publication effect. Verification failures use the existing bounded durable retry schedule; a lost claim cannot schedule work under a replacement lease.

`Repository.CompleteDevelopmentPublication` requires the exact live claim, current worker identity and fencing token, operation and resource identity, effective actor and correlation, and stored reviewed inputs. It acquires the policy and revision locks before the operation lock, then reuses the existing plan, enforcement-catalog, policy, provider-grant, and acceptance verification transaction. The supplied command-owned verifier must read and validate actual enforcement bytes, signed runtime acceptance, and the pinned running deployment. Public request data cannot select or supply that verifier.

Database time is checked after lock acquisition and verification. The verifier's context ends at the earlier lease or operation deadline, with a two-minute maximum. An expired lease, changed worker token, substituted inputs, withdrawn policy, revoked grant, expired acceptance, cancelled context, or failed verification prevents publication. A successful completion commits the terminal operation, revision version, evidence audit, and publication outbox event together. A failed audit write rolls back all of them. Lost acknowledgements are resolved by reading the operation; verification has no external mutation to repeat.

Migration 57 adds the immutable input table and restricts version 3 operations to their declared states. Old reference reconcilers cannot move a version 3 operation into their `applying` or `observing` states. The generic transition path cannot mark it published. Downgrade is refused while any queued-publication request or version 3 operation remains, including terminal history. Upgrade the schema before accepting these internal requests. The explicit consumer below uses the command-owned verifier. The default worker and governed invocation worker do not select this publication path.

PostgreSQL tests cover concurrent acceptance, replay after repository replacement, stale and replaced leases, expiry during verification, scoped immutable pins, policy withdrawal, provider revocation, audit-write rollback, downgrade refusal, exact consumer claims, and worker replacement. These tests use controlled runtime evidence and do not establish live runtime certification or release acceptance. A replacement set of reviewed inputs currently requires a new revision; public administration, authorization refresh, and the complete supported publication journey remain separate work.

## Queue and consume one reviewed publication

Use the prerequisites and environment from [governed development publication](development-publication.md): one exact draft, installed plan and policy, active provider grant, actual enforcement object, signed strict acceptance, and independently pinned running gateway. Both commands load the same deployment-owned configuration and hash all verifier and routing inputs. Neither command starts a gateway, installs a provider, or acquires credentials.

Set `PUBLICATION_DEADLINE` to a reviewed RFC 3339 timestamp after the current database time and no more than 24 hours ahead. Accept the request with:

```sh
go run ./cmd/dataground-worker queue-development-publication \
  --expected-version 1 \
  --plan-digest 'sha256:<reviewed-plan-digest>' \
  --policy-digest 'sha256:<reviewed-effective-policy-digest>' \
  --actor development-operator \
  --correlation-id cor_00000000000000000001 \
  --deadline "$PUBLICATION_DEADLINE"
```

Success prints the queued operation as JSON. It does not run runtime verification or publish the draft. Retry the exact command after a lost acknowledgement; the recorded request and deadline remain authoritative. Copy its `metadata.id` to `PUBLICATION_OPERATION_ID`, retain the same reviewed inputs and environment, and run:

```sh
DATAGROUND_WORKER_ID=publication-worker-a \
go run ./cmd/dataground-worker reconcile-development-publication \
  --expected-version 1 \
  --plan-digest 'sha256:<reviewed-plan-digest>' \
  --policy-digest 'sha256:<reviewed-effective-policy-digest>' \
  --operation-id "$PUBLICATION_OPERATION_ID"
```

The consumer requires a nonempty worker identity and matches its independently loaded inputs against the immutable request before claiming work. It leases only that version 3 publication operation, in its exact domain, service and revision. It cannot consume invocations or another publication. Verification uses the existing signed acceptance and running-deployment checks. Lease acquisition uses PostgreSQL time rather than the worker clock. A two-minute lease accommodates the bounded verifier; the actual operation deadline can shorten it. A competing worker waits while the lease is active. After a crash, a replacement can reclaim the expired lease. It cannot complete under the old token.

The command polls durable state until the operation becomes terminal or the process receives a termination signal. Verification failures use the reconciler's durable retry schedule. Signals cancel verification and leave any uncommitted work for recovery after lease expiry. Completion uses the actor and correlation from the current claim, including an explicit repair, rather than accepting a replacement actor on the consumer command line.

A terminal operation is printed as JSON. `published` returns success; `failed` or `cancelled` returns a nonzero exit status after printing the recorded operation. Restart the same consumer after a lost final acknowledgement to read the terminal operation without another verification or mutation. This result is historical: later acceptance expiry, policy withdrawal, grant revocation or revision retirement does not change it. Invocation readiness still performs its own current checks. A repaired operation is consumed only after the repair command has reopened it with the original immutable reviewed inputs.

These commands provide the internal operator queue and worker composition. Public publication authorization, a complete consumer-facing journey, production backends, and release acceptance remain separate requirements.
