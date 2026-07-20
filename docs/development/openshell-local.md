# Local OpenShell execution boundary

DataGround's first OpenShell development profile is pinned and internally consistent, but it is not yet a completed runtime certification. The profile establishes the Docker gateway topology, immutable OCI inputs, an upstream deny-all policy fixture, the internal `ExecutionProvider` port, and the Codex app-server JSONL-over-stdio transport. The internal Codex adapter has protocol-level conformance coverage for its initial lifecycle, event, interruption, and approval surface. Live sandbox execution and provider-secret non-exposure remain blocked until the profile is exercised on a Docker-capable host with an OpenShell-mediated Codex provider.

The machine-readable record is [`deploy/openshell/development-profile.json`](../../deploy/openshell/development-profile.json). `pnpm openshell:profile:check` verifies its immutable image references, loopback topology, deny-all fixture hash, blocked certification state, runtime transport, and reproducible schema-evidence metadata. The adapter under `internal/execution/openshell` additionally requires the installed CLI to report OpenShell `0.0.86` before a caller admits live work.

## Pinned inputs and provenance

The profile pins OpenShell `v0.0.86` at commit `d556748771c41cbbd4e4dd7cd9030c798afe2b7d`, its gateway and supervisor multi-architecture image indexes, and the OpenShell Community base image at repository commit `fffb6b2248ff6ba585f50517f3711b08122089f2`. That base image contains Codex `0.117.0`, so this is the first runtime version under review even though newer Codex releases may exist.

The deny-all fixture is copied byte-for-byte from `crates/openshell-prover/testdata/empty-policy.yaml` in the pinned OpenShell release. Its SHA-256 digest is recorded in the profile and checked on every `pnpm verify`. It permits negative and lifecycle conformance work without creating a Cedar-to-OpenShell translator. It does not replace Rosetta and is never production-certifiable.

The Codex provider profile digest records the upstream `providers/codex.yaml` input. It is evidence for a later live test, not an authorization to place credentials in DataGround configuration, environment variables, command arguments, logs, or sandbox files. Provider creation is an OpenShell operator responsibility.

Codex schema generation does not produce byte-stable aggregate files because definition order can vary between runs. The profile therefore records a SHA-256 digest after recursive object-key ordering and compact JSON serialization. Regenerate the schema twice and verify either directory with `pnpm codex:schema:check <directory>`; the check also confirms the required initial methods and server requests remain present.

Primary upstream references are the OpenShell [runtime architecture](https://docs.nvidia.com/openshell/about/how-it-works), [sandbox management](https://docs.nvidia.com/openshell/sandboxes/manage-sandboxes), [compute drivers](https://docs.nvidia.com/openshell/reference/sandbox-compute-drivers), [provider configuration](https://docs.nvidia.com/openshell/sandboxes/providers-v2), [credential mediation](https://docs.nvidia.com/openshell/sandboxes/manage-providers), and [security guidance](https://docs.nvidia.com/openshell/security/best-practices). The native runtime contract follows the official Codex [app-server documentation](https://developers.openai.com/codex/app-server).

## Development topology

The initial live topology is one loopback-only OpenShell gateway using the Docker compute driver. Docker Compose mounts the Docker socket and `/var/lib/openshell`; this grants the gateway substantial control over the development host and is unsuitable for an untrusted or shared machine. The gateway uses plaintext transport only on loopback. The provider contract supports multiple registered gateways and durable selection, but production gateway identity, TLS, richer placement constraints, and deployment wiring remain separate work.

On a Docker-capable development host with OpenShell CLI `0.0.86` installed:

```shell
pnpm openshell:profile:check
docker compose -f deploy/openshell/docker-compose.yml up -d
openshell --gateway-endpoint http://127.0.0.1:8080 status
```

The checked-in profile deliberately does not create a provider or run a credential-bearing agent. A deny-all sandbox can be used for lifecycle conformance without provider access:

```shell
openshell --gateway-endpoint http://127.0.0.1:8080 sandbox create \
  --name dg-deny-all \
  --from ghcr.io/nvidia/openshell-community/sandboxes/base@sha256:aeef1c63f00e2913ea002ccb3aaf925f338b5c5d70e63576f0d95c16a138044e \
  --policy deploy/openshell/policies/deny-all.yaml \
  --no-auto-providers \
  --approval-mode manual \
  -- true
```

Stop the local gateway with `docker compose -f deploy/openshell/docker-compose.yml down`. This removes the gateway container but leaves OpenShell state under `/var/lib/openshell`; do not delete that directory as part of routine cleanup.

## Adapter behavior and remaining gate

The development adapter registers gateways, selects only active gateways with required capabilities, reserves deterministic placements, creates deterministic sandboxes, observes ambiguous creation and termination before repetition, starts Codex app-server through non-TTY stdio, retrieves logs, exports files, terminates sandboxes, and lists managed orphans. It invokes the OpenShell binary directly with an argument vector rather than through a shell. Gateway endpoints and native sandbox names remain in protected provider state; returned resources and runtime sessions cannot serialize them.

The Codex adapter under `internal/runtime/codex` performs the stable `initialize`/`initialized`, thread-start, turn-start, and turn-interrupt exchange against bounded JSONL streams. It normalizes lifecycle, assistant text, and item activity without exposing native identifiers. Approval requests receive opaque adapter IDs and remain unanswered until an explicit decision; locked mode denies them without creating an actionable event. Approval and sandbox defaults are locked and read-only. Question, permission-escalation, rich item-delta, usage, resume/steer, and artifact normalization remain outside this initial protocol surface and must not be advertised as supported.

The provider accepts a replaceable state store. Its in-memory implementation is for focused conformance tests only. The PostgreSQL implementation persists isolation-scoped gateway registrations, capabilities, endpoints, drain and loss state, placement reservations, native sandbox routing, observed execution state, and released capacity across provider restarts. Registration and placement retries are idempotent only when immutable inputs match; conflicting retries fail closed. A lost gateway marks its nonterminal executions unknown and its active placements lost instead of reassigning a running sandbox.

Schema migration `00002_execution_placement.sql` establishes this protected routing state. The store is exercised with PostgreSQL integration tests, but it is not yet connected to a public admission command or a production reconciler. Gateway credentials, health leases, capacity signals, locality, policy/runtime compatibility, migration, and stuck-reservation repair remain future placement work.

## Immutable execution plans

The internal `dataground.execution-plan/v1` contract is the portable input to future admission and reconciliation. It binds one isolation-scoped service revision to its runtime profile and required capabilities, an environment revision and immutable OCI image, environment-manifest digest, enforcement-bundle identifier and digest, provider-profile names, and certified runtime-matrix identifier and digest. Lists are normalized before the plan is hashed and stored. A service revision can receive one plan: an identical retry succeeds, while any replacement fails closed.

The binding validates that the runtime profile and capabilities exactly match the referenced service revision. PostgreSQL persists it through `00003_execution_plan.sql`, cascades it when the revision is deleted, and records the successful binding in the audit log in the same transaction. Isolation-domain scope is part of both the primary key and foreign key. The public v1 API is unchanged.

An enforcement-bundle identifier is not a filesystem path. A future governed resolver must retrieve the bundle through DataGround's artifact boundary, verify its digest, and only then materialize a gateway-local file for `ExecutionProvider.Create`. Provider profiles are names, not embedded provider configuration or credentials. The plan deliberately contains no gateway endpoint, sandbox name, native runtime identifier, local policy path, or secret.

This increment does not resolve environment, policy, or runtime-matrix resources; authorize or publish a revision; select a gateway; materialize a policy file; or start a sandbox. Those steps belong to the later finite publication and invocation reconcilers. Missing inputs and unavailable Rosetta translation continue to fail closed.

## Rosetta candidate client

The internal client under `internal/policy/rosetta` implements the HTTP surface observed at Rosetta commit `320158f1e4a4eea378d82c1527f4a7af5fb9855b`: compiler `1.0.0`, catalog `rosetta/v1`, and OpenShell target contract `rosetta/openshell-policy-v1`. It sends only strict compile requests, requires HTTPS except for explicit loopback tests, disables redirects, bounds request and response bodies, and accepts workload identity or mTLS only through an operator-supplied HTTP transport. It has no bearer-token or provider-credential configuration.

Successful responses must match the pinned compiler and target contracts, include one plain `policy.yaml`, provide exactly one decision for every requested capability, and pass independent YAML validation. The client recomputes Rosetta's deterministic input hash and the artifact hash, then creates a separate DataGround binding digest covering the isolation domain, revision or execution, input hash, and artifact hash. Upstream diagnostic messages and error bodies are never exposed through the adapter.

This client is conformance scaffolding, not a production integration. No publication, admission, reconciliation, gateway, or public API path can call it. Rosetta has not published the corresponding release tag, immutable service image, stable service error codes, authenticated deployment profile, or signed conformance fixtures.

A real runtime invocation is not certified until all of the following evidence exists:

1. The pinned images and CLI run on the target Docker profile.
2. Codex app-server completes initialize, thread, turn, event, interruption, approval, and artifact flows through the adapter on the real gateway; the in-memory protocol suite is necessary evidence but is not a live certification.
3. Restart, drain, gateway loss, timeout, lost acknowledgement, export-before-teardown, and orphan cleanup tests pass against the real gateway.
4. Process, environment, filesystem, argument, log, and error inspection prove that a sandbox and Codex process cannot read a provider key or refresh token.
5. Browser and public API tests prove that gateway URLs, sandbox ports, provider-native IDs, and runtime endpoints never cross the DataGround contract.

Production-certified Rosetta remains unavailable, so any runtime work requiring generated enforcement material must fail closed. The next live gates are a tagged Rosetta release with authenticated transport and differential fixtures, followed by a Docker-hosted conformance run with OpenShell-mediated credentials. Neither gate permits a fallback policy compiler.
