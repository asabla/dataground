# DataGround

DataGround is a self-hosted-first data, notebook, and agent-execution platform. It combines a platform-owned control plane with governed OpenShell sandboxes, Cedar authorization, native agent-runtime adapters, durable resource reconciliation, and open data protocols.

The repository contains an executable project foundation: a Go API process, a TypeScript/React workbench shell, versioned native contracts, an owned token and accessible-component foundation, a deterministic in-memory reference runtime, pinned toolchains, and continuous verification. The reference path establishes agent services, immutable revisions, aliases, invocations, typed event replay, cancellation, artifacts, and usage without a provider or OpenShell dependency. Persistence, authorization, audit, and infrastructure integrations remain intentionally unimplemented.

## Start here

- [System specification](docs/architecture/system-specification.md) defines the product, security, runtime, data, API, and operational contracts.
- [Architecture decision register](docs/architecture/decision-register.md) records confirmed decisions and overrides conflicting proposal language in the specification.
- [Implementation starter](docs/implementation/README.md) translates the architecture into prerequisites, an initial vertical slice, design-system guidance, verification gates, and handoff prompts.
- [Agent guidance](AGENTS.md) defines repository-wide constraints for coding agents and contributors using them.

Proposed implementation choices in the starter are not confirmed architecture. Exact dependency versions, image digests, capability profiles, and measured capacity belong in signed release certification manifests once implementation begins.

## Development

The pinned baseline is Go 1.26.5, Node.js 24 LTS, and pnpm 11.13.1. The exact Go and Node versions are recorded in `.go-version` and `.nvmrc`; `package.json` records the pnpm version.

```shell
pnpm install --frozen-lockfile
pnpm verify
```

`pnpm verify` runs formatting checks, linting, contract and generated-artifact drift checks, type checking, tests, Storybook, and production builds. Run the reference API with `pnpm dev:api`, the workbench with `pnpm dev:workbench`, and the component contract surface with `pnpm dev:design-system`. The API listens on `127.0.0.1:8080` by default; set `DATAGROUND_HTTP_ADDRESS` explicitly when another bind address is required. The reference API has no authentication and must not be exposed as a production service.

See [development guidance](docs/development/README.md) for the repository layout and command contract, [design-system guidance](docs/development/design-system.md) for token and component maintenance, and [reference runtime guidance](docs/development/reference-runtime.md) for the implemented lifecycle and limitations.
