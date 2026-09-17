# Publish a governed development revision

`go run ./cmd/dataground-worker publish-development` publishes one existing draft Codex revision after checking its reviewed inputs. This is an internal, database-authorized operator command for the strict Linux ARM64 local acceptance profile. The public publication API remains reference-only. Production publication, deployment readiness, traffic rollout, and release acceptance remain separate work.

Create the service and draft revision through the durable API. The revision must declare runtime profile `codex.app-server/v1` and exactly that required capability. Install its approval-capable version 3 invocation policy, [strict enforcement object](development-enforcement.md), [execution plan](governed-worker.md#install-a-reviewed-execution-plan), and current `codex` provider-profile grant. Prepare the independently pinned [running gateway](development-gateway.md) and signed version 2 local acceptance described in [governed worker guidance](governed-worker.md#use-strict-local-acceptance-with-a-pinned-deployment).

Set all `DATAGROUND_LOCAL_RUNTIME_*` acceptance and deployment inputs from that profile, `DATAGROUND_DEVELOPMENT_ISOLATION_DOMAIN_ID`, `DATAGROUND_DEVELOPMENT_RUNTIME_PROFILE=openshell-codex-strict-candidate-development/v1`, `DATAGROUND_DATABASE_URL`, `DATAGROUND_S3_ENDPOINT`, and `DATAGROUND_S3_BUCKET`. Run from the repository root. The command uses the existing signed acceptance verifier and running-deployment observer; it does not generate acceptance evidence, sign an envelope, start a gateway, or acquire provider credentials. The S3 endpoint must use loopback HTTP, with anonymous path-style access; redirects and ambient HTTP proxies are disabled.

Review the normalized execution-plan digest and effective invocation-policy digest, including its current entity activation. Then run:

```sh
go run ./cmd/dataground-worker publish-development \
  --expected-version 1 \
  --plan-digest 'sha256:<reviewed-plan-digest>' \
  --policy-digest 'sha256:<reviewed-effective-policy-digest>' \
  --actor development-operator \
  --correlation-id cor_00000000000000000001
```

The exact isolation domain, service, and revision come from the accepted runtime configuration. The command hashes every acceptance, deployment, verifier, and storage input into its replay identity. The database transaction locks the policy scope, draft revision, and provider grant. It checks the immutable plan and enforcement catalog binding, resolves the current unwithdrawn policy, reads and hashes the actual S3 policy bytes, and verifies the signed acceptance and current gateway. It then checks PostgreSQL time against both acceptance and grant expiry. Pending grant revocation, policy withdrawal or entity activation is ordered through the same administrative locks.

Publication has no external mutation. One transaction changes the revision to `published`, increments its version, records a terminal version 2 publication operation, writes append-only acceptance evidence and the publication audit, schedules the publication outbox event, and stores the exact replay result. Version 1 reference publication retains its existing reconciler. A failed check, cancelled request, or failed database write leaves the draft unchanged. The complete command has a two-minute deadline; verification holds the scope locks within that bound.

Success prints one `dataground.development-publication/v1` JSON receipt containing `isolationDomainId`, `serviceId`, `revisionId`, `operationId`, `state: published`, and `certificationEligible: false`. Audit evidence records the reviewed digests, acceptance identity, generation and expiry, and provider-grant generation. Policy bytes, credentials, object keys, verifier paths, and deployment endpoints are not emitted.

Retry the complete original command after a lost acknowledgement or failed stdout write. Exact replay returns the original receipt without external verification or another mutation. It remains historical after later withdrawal, revocation, expiry, or retirement and cannot republish a retired revision. Changing the reviewed inputs, actor, or correlation is not exact replay. Read the returned operation through the existing operation API to inspect the recorded publication result.

After publication, the existing public alias API can route to the revision, and the durable API can select it with the governed dispatch configuration. The worker still checks runtime acceptance, policy authorization, provider grants, enforcement material, and claim ownership at execution boundaries. Publication does not guarantee that an invocation will start: the actual OpenShell `codex` provider configuration and inference route must already exist, and deployment failures remain subject to those checks. This command does not claim the full normative production publication validation or a certified release.
