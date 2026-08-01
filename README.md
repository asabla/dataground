# DataGround

DataGround is a self-hosted-first data, notebook, and agent-execution platform. It combines a platform-owned control plane with governed OpenShell sandboxes, Cedar authorization, native agent-runtime adapters, durable resource reconciliation, and open data protocols.

The repository contains an executable project foundation: a Go API process, a TypeScript/React workbench shell, versioned native contracts, an owned token and accessible-component foundation, deterministic process-local and PostgreSQL-backed reference runtimes, durable reconcilers, audit/outbox persistence, pinned toolchains, and continuous verification. A development-only OpenShell execution-provider boundary, durable PostgreSQL gateway-placement store, and immutable local profile are present, but no public OpenShell admission path, live runtime, or provider credential path is certified yet. A loopback-only development bearer identity and static Cedar action policy now protect the API; durable mode additionally withholds completed decisions until their correlation and immutable policy provenance are recorded in PostgreSQL. Durable mode can also retain one audited, immutable invocation Cedar policy per exact service revision, withhold completed effect-time Cedar evaluations until their exact policy provenance and durable scope are recorded, and export frozen isolation-scoped pages from both authorization decision streams through an audited internal operator command with append-only receipts. An explicit loopback-only governed development worker can compose the complete version 2 invocation lifecycle for one configured isolation domain, while reference mode remains the default. An internal provider-neutral OIDC boundary can bind verifier-accepted issuer/subject/audience identity to platform-owned principal and isolation-domain data without trusting token-carried scope. Concrete signature and discovery verification, identity registry storage, authentication audit, replay-resistant transport, API composition, policy authoring and distribution, entity loading, audit transport, acknowledgement, access and retention policy, default or public governed execution, production artifact storage, and production infrastructure integrations remain intentionally unimplemented.

## Start here

- [System specification](docs/architecture/system-specification.md) defines the product, security, runtime, data, API, and operational contracts.
- [Architecture decision register](docs/architecture/decision-register.md) records confirmed decisions and overrides conflicting proposal language in the specification.
- [Implementation starter](docs/implementation/README.md) translates the architecture into prerequisites, an initial vertical slice, design-system guidance, verification gates, and handoff prompts.
- [Agent guidance](AGENTS.md) defines repository-wide constraints for coding agents and contributors using them.

Proposed implementation choices in the starter are not confirmed architecture. Exact dependency versions, image digests, capability profiles, and measured capacity belong in signed release certification manifests once implementation begins.

## Development

The pinned baseline is Go 1.26.5, Node.js 24 LTS, and pnpm 11.15.0. The exact Go and Node versions are recorded in `.go-version` and `.nvmrc`; `package.json` records the pnpm version.

```shell
pnpm install --frozen-lockfile
pnpm verify
```

`pnpm verify` runs formatting checks, linting, contract and generated-artifact drift checks, type checking, tests, Storybook, and production builds. Run the reference API with the explicit development identity variables documented in [development guidance](docs/development/README.md); the underlying command is `pnpm dev:api`. Run the workbench with `pnpm dev:workbench` and the component contract surface with `pnpm dev:design-system`. The API listens on `127.0.0.1:8080` by default; set `DATAGROUND_HTTP_ADDRESS` explicitly when another bind address is required. The reference API requires an explicitly configured loopback-only development bearer identity and binds it to a static development Cedar action policy. These adapters are not production identity or policy services, and the API must not be exposed as a production endpoint. PostgreSQL-backed durable mode records completed action decisions; process-local mode remains ephemeral.

See [development guidance](docs/development/README.md) for the repository layout and command contract, [design-system guidance](docs/development/design-system.md) for token and component maintenance, [reference runtime guidance](docs/development/reference-runtime.md) for the implemented lifecycle, and [local OpenShell guidance](docs/development/openshell-local.md) for the pinned but blocked execution profile.

