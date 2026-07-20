# Local OpenShell execution boundary

DataGround's first OpenShell development profile is pinned and internally consistent, but it is not yet a completed runtime certification. The profile establishes the Docker gateway topology, immutable OCI inputs, an upstream deny-all policy fixture, the internal `ExecutionProvider` port, and the Codex app-server JSONL-over-stdio transport. Live sandbox execution and provider-secret non-exposure remain blocked until the profile is exercised on a Docker-capable host with an OpenShell-mediated Codex provider.

The machine-readable record is [`deploy/openshell/development-profile.json`](../../deploy/openshell/development-profile.json). `pnpm openshell:profile:check` verifies its immutable image references, loopback topology, deny-all fixture hash, blocked certification state, and runtime transport. The adapter under `internal/execution/openshell` additionally requires the installed CLI to report OpenShell `0.0.86` before a caller admits live work.

## Pinned inputs and provenance

The profile pins OpenShell `v0.0.86` at commit `d556748771c41cbbd4e4dd7cd9030c798afe2b7d`, its gateway and supervisor multi-architecture image indexes, and the OpenShell Community base image at repository commit `fffb6b2248ff6ba585f50517f3711b08122089f2`. That base image contains Codex `0.117.0`, so this is the first runtime version under review even though newer Codex releases may exist.

The deny-all fixture is copied byte-for-byte from `crates/openshell-prover/testdata/empty-policy.yaml` in the pinned OpenShell release. Its SHA-256 digest is recorded in the profile and checked on every `pnpm verify`. It permits negative and lifecycle conformance work without creating a Cedar-to-OpenShell translator. It does not replace Rosetta and is never production-certifiable.

The Codex provider profile digest records the upstream `providers/codex.yaml` input. It is evidence for a later live test, not an authorization to place credentials in DataGround configuration, environment variables, command arguments, logs, or sandbox files. Provider creation is an OpenShell operator responsibility.

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

The provider accepts a replaceable state store. Its in-memory implementation is for focused conformance tests only. The PostgreSQL implementation persists isolation-scoped gateway registrations, capabilities, endpoints, drain and loss state, placement reservations, native sandbox routing, observed execution state, and released capacity across provider restarts. Registration and placement retries are idempotent only when immutable inputs match; conflicting retries fail closed. A lost gateway marks its nonterminal executions unknown and its active placements lost instead of reassigning a running sandbox.

Schema migration `00002_execution_placement.sql` establishes this protected routing state. The store is exercised with PostgreSQL integration tests, but it is not yet connected to a public admission command or a production reconciler. Gateway credentials, health leases, capacity signals, locality, policy/runtime compatibility, migration, and stuck-reservation repair remain future placement work.

A real runtime invocation is not certified until all of the following evidence exists:

1. The pinned images and CLI run on the target Docker profile.
2. Codex app-server completes initialize, thread, turn, event, interruption, and artifact flows through the adapter.
3. Restart, drain, gateway loss, timeout, lost acknowledgement, export-before-teardown, and orphan cleanup tests pass against the real gateway.
4. Process, environment, filesystem, argument, log, and error inspection prove that a sandbox and Codex process cannot read a provider key or refresh token.
5. Browser and public API tests prove that gateway URLs, sandbox ports, provider-native IDs, and runtime endpoints never cross the DataGround contract.

Rosetta remains unavailable, so any runtime work requiring generated enforcement material must fail closed. The next live gate is a Docker-hosted conformance run with OpenShell-mediated credentials; it is not a fallback policy compiler.
