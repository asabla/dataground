# Development

## Toolchains

DataGround currently uses Go 1.26.5, Node.js 24 LTS, and pnpm 11.15.0. Version managers may read `.go-version` and `.nvmrc`; Corepack or another pnpm installation must honor the exact `packageManager` field in `package.json`.

PostgreSQL 18 is required for durable integration tests and durable control-plane operation. The ordinary local verification command skips database integration tests when `DATAGROUND_TEST_DATABASE_URL` is absent; CI requires them. The OpenShell CLI and Docker are required only for the blocked local execution profile described in [local OpenShell guidance](openshell-local.md); they are not required by the ordinary reference runtime or CI. Kubernetes and Rosetta are not runtime dependencies.

## Repository layout

| Path | Responsibility |
| --- | --- |
| `cmd/dataground-api` | Go API process entry point |
| `cmd/dataground-worker` | Replaceable publication and invocation reconciler |
| `cmd/dataground-migrate` | PostgreSQL schema migration and compatibility check |
| `cmd/dataground-repair` | Audited failed-operation repair command |
| `internal` | Platform-owned Go implementation packages |
| `deploy/openshell` | Pinned, loopback-only OpenShell development profile and deny-all fixture |
| `apps/workbench` | TypeScript/React workbench application |
| `packages/tokens` | DTCG token sources, deterministic generator, themes and densities |
| `packages/ui` | Accessible React primitives, component styles and Storybook contracts |
| `contracts/openapi` | Canonical versioned public HTTP contract |
| `contracts/schemas` | Canonical versioned non-HTTP JSON Schemas |
| `contracts/fixtures` | Valid and invalid contract examples |
| `contracts/compatibility` | Frozen public compatibility baselines |
| `scripts` | Dependency-light repository checks |
| `docs/architecture` | Normative specification and decisions |
| `docs/implementation` | Sequencing, proposals, gates and handoff prompts |

Do not create directories from the proposed blueprint until code has a verified consumer for them.

## Commands

Install exactly the locked JavaScript dependency graph:

```shell
pnpm install --frozen-lockfile
```

Run the complete local and CI baseline:

```shell
pnpm verify
```

The verification command runs Biome formatting and lint checks, `gofmt`, `go vet`, OpenAPI and JSON Schema fixture and compatibility checks, generated contract and token drift checks, TypeScript type checking, Go and Node tests, Playwright-backed Storybook interaction and accessibility tests, the Storybook production build, and production builds for the API and workbench.

It also verifies that the blocked OpenShell development profile remains immutable and internally consistent. This is a configuration check, not a live runtime certification.

Regenerate the typed workbench contract after an accepted OpenAPI change:

```shell
pnpm contracts:generate
```

The process-local deterministic server remains available for contract work:

```shell
pnpm dev:api
pnpm dev:workbench
pnpm dev:design-system
pnpm --filter @dataground/ui test:stories
```

The API listens on `127.0.0.1:8080` by default. Process-local mode refuses non-loopback binding and exposes the in-memory reference agent-service lifecycle described in [reference runtime guidance](reference-runtime.md). The workbench uses Vite's development server. Storybook documents shared component contracts and is not a product application.

To run durable mode, migrate a PostgreSQL database and start the API and worker with the same `DATAGROUND_DATABASE_URL`:

```shell
DATAGROUND_DATABASE_URL='postgres://dataground:dataground@127.0.0.1:5432/dataground?sslmode=disable' go run ./cmd/dataground-migrate up
DATAGROUND_DATABASE_URL='postgres://dataground:dataground@127.0.0.1:5432/dataground?sslmode=disable' go run ./cmd/dataground-api
DATAGROUND_DATABASE_URL='postgres://dataground:dataground@127.0.0.1:5432/dataground?sslmode=disable' go run ./cmd/dataground-worker
```

Durable mode persists resources, idempotency results, explicit publication and invocation operations, event replay, external-effect receipts, outbox events, and audit records. It may use `DATAGROUND_HTTP_ADDRESS` for an explicit non-loopback bind, but it still has no authentication or Cedar authorization boundary and must remain behind a trusted development network boundary. See [durable control-plane operations](durable-control-plane.md).

See [design-system guidance](design-system.md) before changing token source, component APIs, themes, density behavior, or Storybook configuration.

## Contract changes

The OpenAPI and release-manifest files are compatibility surfaces even in alpha form. Change their schema identities or semantics deliberately, update examples and checks together, and do not add public infrastructure endpoints or provider-native concepts. See [`contracts/README.md`](../../contracts/README.md) for the compatibility rules and generation workflow.
