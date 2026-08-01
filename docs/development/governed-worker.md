# Governed development worker

`dataground-worker` keeps the deterministic PostgreSQL reference driver as its default. An explicit `governed-development` mode composes the complete version 2 invocation lifecycle through the existing OpenShell admission, Codex runtime, cancellation, Cedar authorization, decision-audit, and artifact-finalization boundaries. Publication effects and version 1 invocation effects continue to use the deterministic reference driver.

This mode is intentionally limited to one development isolation domain, the pinned OpenShell `0.0.86` Docker profile at `http://127.0.0.1:8080`, provider profile `codex`, and anonymous path-style S3 on a loopback plaintext endpoint. Admission additionally requires runtime profile and gateway capability `codex.app-server/v1` plus the development profile's exact digest-pinned sandbox image; profile drift fails before provider placement. The mode does not add workload authentication, provider provisioning, production policy distribution, Rosetta materialization, audit export or retention, backend certification, or public execution guarantees. The runtime-conformance record and production-certified Rosetta release remain separate gates.

The PostgreSQL schema must be current. The exact service revision must already have an immutable execution plan, enforcement-bundle catalog record and object, and installed invocation Cedar policy. The configured OpenShell gateway and `codex` provider must already exist. The repository does not yet expose a supported public workflow for provisioning those execution inputs; missing or unavailable inputs fail closed through the durable retry or terminal-failure contracts.

Select the mode with the complete environment below:

```sh
DATAGROUND_DATABASE_URL='postgres://dataground:dataground@127.0.0.1:5432/dataground?sslmode=disable' \
DATAGROUND_WORKER_MODE='governed-development' \
DATAGROUND_DEVELOPMENT_ISOLATION_DOMAIN_ID='iso_00000000000000000001' \
DATAGROUND_OPENSHELL_GATEWAY_ID='gw_00000000000000000001' \
DATAGROUND_OPENSHELL_GATEWAY_ENDPOINT='http://127.0.0.1:8080' \
DATAGROUND_OPENSHELL_POLICY_WORKSPACE='/absolute/private/policy-workspace' \
DATAGROUND_OPENSHELL_EXPORT_WORKSPACE='/absolute/private/export-workspace' \
DATAGROUND_S3_ENDPOINT='http://127.0.0.1:8333' \
DATAGROUND_S3_BUCKET='dataground-development' \
DATAGROUND_S3_REQUEST_TIMEOUT='2m' \
DATAGROUND_INVOCATION_ARTIFACT_MAX_BYTES='16777216' \
go run ./cmd/dataground-worker
```

`DATAGROUND_OPENSHELL_BINARY` may select the installed CLI path; it defaults to `openshell`. Both network endpoints must be plain HTTP loopback origins without embedded credentials, paths, queries, or fragments. The worker disables proxy inheritance for S3. The S3 request timeout must be between one second and ten minutes. The policy and export workspaces must be distinct non-root absolute paths; their workspace implementations additionally require owner-held mode-0700 directories and exclusive locks. The artifact limit must be between one byte and one GiB.

Startup requires the exact database schema, validates the complete mode configuration, acquires both private workspaces, composes every governed dependency, verifies the pinned OpenShell CLI version, and idempotently registers the exact gateway configuration before polling begins. The worker leases operations only from the configured isolation domain; it cannot advance attempts or state for unrelated domains. It does not turn dependency availability into a false startup guarantee: gateway and object-store outages retain their existing durable reconciliation behavior.

Admission, runtime, and cancellation share the same exact-revision PostgreSQL policy source and audited Cedar authorizer. A completed allow, deny, or evaluator-unavailable outcome is withheld until its append-only invocation-decision row commits. The runtime route remains claim-bound, consumes one durable attempt before starting Codex, renews the exact lease, persists normalized events, validates output, exports declared artifacts through the private workspace, and verifies immutable object storage before catalog binding. Any missing phase or typed-nil dependency prevents worker startup; no version 2 phase can silently fall back to the reference driver.
