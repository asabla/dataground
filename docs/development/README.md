# Development

## Toolchains

DataGround currently uses Go 1.26.5, Node.js 24 LTS, and pnpm 11.13.1. Version managers may read `.go-version` and `.nvmrc`; Corepack or another pnpm installation must honor the exact `packageManager` field in `package.json`.

The repository does not require Docker, Kubernetes, PostgreSQL, OpenShell, or Rosetta for bootstrap verification. Those dependencies are introduced only with their contract and conformance tests.

## Repository layout

| Path | Responsibility |
| --- | --- |
| `cmd/dataground-api` | Go API process entry point |
| `internal` | Platform-owned Go implementation packages |
| `apps/workbench` | TypeScript/React workbench application |
| `contracts/openapi` | Versioned platform OpenAPI documents |
| `contracts/schemas` | Versioned JSON Schemas |
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

The verification command runs Biome formatting and lint checks, `gofmt`, `go vet`, structural OpenAPI and JSON Schema checks, TypeScript type checking, Go and Vitest tests, and production builds for the API and workbench.

Start the bootstrap processes separately:

```shell
pnpm dev:api
pnpm dev:workbench
```

The API uses `DATAGROUND_HTTP_ADDRESS` when set and otherwise listens only on `127.0.0.1:8080`. Its current endpoints are `GET /livez` and `GET /readyz`. The workbench uses Vite's development server. Neither process currently implements authentication, product resources, persistence, or infrastructure integration. Any non-loopback API bind must be an explicit deployment decision with the appropriate network boundary.

## Contract changes

The OpenAPI and release-manifest files are compatibility surfaces even in alpha form. Change their schema identities or semantics deliberately, update examples and checks together, and do not add public infrastructure endpoints or provider-native concepts. `pnpm contracts:check` is the current structural floor, not a substitute for the full compatibility tooling required before product contracts stabilize.
