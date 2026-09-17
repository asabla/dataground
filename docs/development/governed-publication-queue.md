# Durable governed publication boundary

The internal repository supports a queued form of the [governed development publication](development-publication.md) transaction. This implements the durable publication boundary needed by ADR-003 and ADR-038. The worker command exposes this boundary through explicit database-authorized operator commands. An explicit loopback API configuration also composes the [publication authorization boundary](publication-authorization.md) for version 4 operations, as described below. `publish-development` retains its atomic operator behavior.

`Repository.QueueDevelopmentPublication` accepts an idempotent operator request containing one exact isolation domain, service, draft revision, expected revision version, plan digest, policy digest, and verification-configuration digest. The target must use `codex.app-server/v1` with that exact required capability. The request creates a version 3 publication operation and immutable reviewed-input row, with the acceptance audit and outbox event in the same transaction. It returns the existing operation representation with status 202. The draft does not become callable. Acceptance does not prove runtime readiness or grant publication authority to a public caller.

The idempotency digest binds the reviewed inputs and actor. An exact retry returns the original acceptance response, even after completion. A newly generated correlation or deadline does not replace the recorded values. Changed reviewed inputs conflict. The deadline must be current and within 24 hours. The original operation remains authoritative for current state; the stored acceptance response is historical.

Version 3 uses `queued → validating → published`. Failure and cancellation remain terminal alternatives, and explicit repair retains the original reviewed inputs. The reconciler requires a `GovernedPublicationDriver` before advancing this path. It never substitutes a reference publication receipt or a native publication effect. Verification failures use the existing bounded durable retry schedule; a lost claim cannot schedule work under a replacement lease.

`Repository.CompleteDevelopmentPublication` requires the exact live claim, current worker identity and fencing token, operation and resource identity, effective actor and correlation, and stored reviewed inputs. It acquires the policy and revision locks before the operation lock, then reuses the existing plan, enforcement-catalog, policy, provider-grant, and acceptance verification transaction. The supplied command-owned verifier must read and validate actual enforcement bytes, signed runtime acceptance, and the pinned running deployment. Public request data cannot select or supply that verifier.

Database time is checked after lock acquisition and verification. The verifier's context ends at the earlier lease or operation deadline, with a two-minute maximum. An expired lease, changed worker token, substituted inputs, withdrawn policy, revoked grant, expired acceptance, cancelled context, or failed verification prevents publication. A successful completion commits the terminal operation, revision version, evidence audit, and publication outbox event together. A failed audit write rolls back all of them. Lost acknowledgements are resolved by reading the operation; verification has no external mutation to repeat.

Migration 57 adds the immutable input table and restricts version 3 operations to their declared states. Old reference reconcilers cannot move a version 3 operation into their `applying` or `observing` states. The generic transition path cannot mark it published. Downgrade is refused while any queued-publication request or version 3 operation remains, including terminal history. Upgrade the schema before accepting these internal requests. The explicit consumer below uses the command-owned verifier. The default worker and governed invocation worker do not select this publication path.

PostgreSQL tests cover concurrent acceptance, replay after repository replacement, stale and replaced leases, expiry during verification, scoped immutable pins, policy withdrawal, provider revocation, audit-write rollback, downgrade refusal, exact consumer claims, and worker replacement. These tests use controlled runtime evidence and do not establish live runtime certification or release acceptance. A replacement set of reviewed inputs currently requires a new revision; public administration, replacement of reviewed inputs after authorization refresh, and production publication remain separate work.

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

These version 3 commands retain the internal operator boundary. Use the separate version 4 profile below for authenticated public API acceptance. Production backends and release acceptance remain separate requirements.


## Publish through the authenticated API

The explicit development API profile can start with one exact reviewed draft and accept `POST /v1/isolation-domains/{isolationDomainId}/service-revisions/{revisionId}/actions/publish`. Prepare its plan, enforcement object, provider grant and signed strict runtime acceptance as above, but install a new version 5 policy with `--publication-capable`. That policy must authorize the authenticated publisher at both entry and effect; include the required invocation permissions separately. The existing API action authorizer and domain-membership check also apply.

With the same strict worker environment, prepare the deployment-owned API configuration:

```sh
umask 077
go run ./cmd/dataground-worker prepare-publication-configuration \
  --expected-version 1 \
  --plan-digest 'sha256:<reviewed-plan-digest>' \
  --policy-digest 'sha256:<reviewed-effective-v5-policy-digest>' \
  > /absolute/private/publication.json
```

This command validates and hashes configuration only. It does not connect to PostgreSQL or verify runtime readiness. The output uses contract `dataground.api-governed-publication/v1` and contains `isolationDomainId`, `serviceId`, `revisionId`, `runtimeProfile`, `expectedVersion`, `planDigest`, `policyDigest`, and `verificationDigest`. It contains no actor, runtime paths, native endpoints or credentials. Review the output and set `DATAGROUND_GOVERNED_PUBLICATION_CONFIG_FILE` to that owner-only regular file when starting the durable API. Do not also set `DATAGROUND_GOVERNED_DISPATCH_CONFIG_FILE`. Both development bearer authentication and the separately certified OIDC/DPoP profile support this target; all their existing startup and loopback restrictions still apply.

The public request body remains `{"expectedVersion":1}` with the normal authentication, content type and idempotency headers. The API derives the actor from authentication and takes every digest from its immutable configuration. Invalid schemas return their existing safe 409 codes. A publication denial returns `403 PUBLICATION_FORBIDDEN`; unavailable policy, audit or reviewed dependencies return retryable `503 PUBLICATION_UNAVAILABLE`. Acceptance returns a version 4 operation with status 202 and a 15-minute deadline. It leaves the revision in draft. Each retry checks current publication authority before returning the historical acceptance response. Entity refresh changes the policy digest and invalidates the old reviewed configuration; withdrawal or audit failure also withholds replay.

Read the operation identity from the response and consume it with the independently configured worker:

```sh
DATAGROUND_WORKER_ID=authorized-publication-worker \
go run ./cmd/dataground-worker reconcile-authorized-publication \
  --expected-version 1 \
  --plan-digest 'sha256:<reviewed-plan-digest>' \
  --policy-digest 'sha256:<reviewed-effective-v5-policy-digest>' \
  --operation-id "$PUBLICATION_OPERATION_ID"
```

This consumer leases only the exact version 4 request. It uses the durable actor and correlation, verifies the runtime and current version 5 policy, then records a separate effect decision under the policy and lifecycle locks before publication commits. The older operator consumer cannot claim or complete this operation. Missing authority, a stale lease, expiry, withdrawal, cancellation or audit failure keeps the draft unpublished. Success atomically commits the published revision, terminal operation, runtime evidence audit and outbox event. The same recovery and terminal-observation rules described for the operator consumer apply.

Migration 59 admits and retains version 4 operations, and blocks downgrade while any such history exists. Generic and older reference transition paths cannot publish them. After publication, the configured API can accept governed invocations for this revision through a published alias. An API restart requires the same retained reviewed publication inputs. The separate dispatch-only API and invocation worker still require a published revision at startup. Operator provisioning, live signed runtime evidence, production administration, public policy distribution and release acceptance remain prerequisites outside this development journey.
