# DataGround

DataGround is a self-hosted-first data, notebook, and agent-execution platform. It combines a platform-owned control plane with governed OpenShell sandboxes, Cedar authorization, native agent-runtime adapters, durable resource reconciliation, and open data protocols.

The repository is at its architecture-foundation stage. It does not yet contain application code, build manifests, deployment configuration, or verified build and test commands. The first product slice will establish a governed agent service and its native event contract before broader notebook, lakehouse, job, and compatibility work.

## Start here

- [System specification](docs/architecture/system-specification.md) defines the product, security, runtime, data, API, and operational contracts.
- [Architecture decision register](docs/architecture/decision-register.md) records confirmed decisions and overrides conflicting proposal language in the specification.
- [Implementation starter](docs/implementation/README.md) translates the architecture into prerequisites, an initial vertical slice, design-system guidance, verification gates, and handoff prompts.
- [Agent guidance](AGENTS.md) defines repository-wide constraints for coding agents and contributors using them.

Proposed implementation choices in the starter are not confirmed architecture. Exact dependency versions, image digests, capability profiles, and measured capacity belong in signed release certification manifests once implementation begins.
