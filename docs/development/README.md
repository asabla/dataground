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
| `cmd/dataground-policy-install` | Audited immutable invocation-policy installer |
| `cmd/dataground-auth-rate-limit-policy` | Audited authentication admission-policy activator |
| `cmd/dataground-auth-rate-limit-capacity` | Disposable PostgreSQL admission-capacity evidence runner |
| `cmd/dataground-oidc-provider-credential` | Audited isolation-scoped provider credential publisher |
| `cmd/dataground-provider-dpop-issuance` | External-key provider DPoP issuance evidence verifier |
| `cmd/dataground-audit-export` | Isolation-scoped authorization-decision exporter |
| `cmd/dataground-audit-export-seal` | External-key authorization/operator audit export sealer |
| `cmd/dataground-audit-export-encrypt` | Recipient-bound audit export package encryption |
| `cmd/dataground-audit-export-delivery` | Audited encrypted-package preparation and recipient acknowledgement verifier |
| `cmd/dataground-audit-export-workload-identity` | Audited workload identity grant activation, rotation, and local revocation |
| `cmd/dataground-audit-export-workload-identity-revocation` | Audited external issuer-revocation notice intake |
| `cmd/dataground-audit-export-revocation-import` | Authenticated digest-pinned external revocation notice acquisition |
| `cmd/dataground-audit-export-revocation-authority` | Audited recipient-proof and workload revocation-authority governance |
| `cmd/dataground-audit-export-proofing-authority` | Audited recipient proofing-authority governance |
| `cmd/dataground-audit-export-recipient-trust` | Audited recipient trust activation, rotation, and revocation |
| `cmd/dataground-operator-audit-export` | Isolation-scoped operator-audit exporter |
| `internal` | Platform-owned Go implementation packages |
| `deploy/openshell` | Pinned, loopback-only OpenShell development profile and deny-all fixture |
| `deploy/storage` | Pinned, disposable S3 enforcement-object conformance candidate |
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

It also verifies that the blocked OpenShell and S3 development profiles remain immutable and internally consistent. The ordinary baseline does not start either dependency. GitHub CI separately exercises the enforcement-object transport, invocation-artifact transport, and composed invocation-artifact finalizer/catalog acknowledgement-recovery subsets against the pinned disposable S3 and PostgreSQL candidates; none is a production certification.

Regenerate the typed workbench contract after an accepted OpenAPI change:

```shell
pnpm contracts:generate
```

The process-local deterministic server remains available for contract work:

```shell
DATAGROUND_DEVELOPMENT_BEARER_TOKEN='<development-token-at-least-32-bytes>' \
DATAGROUND_DEVELOPMENT_PRINCIPAL_ID='usr_00000000000000000001' \
DATAGROUND_DEVELOPMENT_ISOLATION_DOMAIN_ID='iso_00000000000000000001' \
pnpm dev:api
pnpm dev:workbench
pnpm dev:design-system
pnpm --filter @dataground/ui test:stories
```

The API listens on `127.0.0.1:8080` by default. Process-local mode refuses non-loopback binding and exposes the in-memory reference agent-service lifecycle described in [reference runtime guidance](reference-runtime.md). Every `/v1` request must use the configured development bearer token. The startup credential is removed from the API process environment and represented only by its digest after assembly. This static single-domain verifier and its exact-principal Cedar action policy are local boundaries, not production identity or policy services. Process-local mode has no durable decision audit. See [API action authorization](api-authorization.md) for the closed route mapping, audit boundary, and failure semantics. The workbench uses Vite's development server. Storybook documents shared component contracts and is not a product application.

The internal enforcement-object S3 protocol boundary and its remaining backend certification requirements are documented in [S3 enforcement-object guidance](s3-enforcement-objects.md). The narrower invocation-artifact transport evidence and exclusions are documented in [S3 invocation-artifact guidance](s3-invocation-artifacts.md).

To run durable mode, migrate a PostgreSQL database and start the API and worker with the same `DATAGROUND_DATABASE_URL`:

```shell
DATAGROUND_DATABASE_URL='<postgresql-url>' go run ./cmd/dataground-migrate up
DATAGROUND_DATABASE_URL='<postgresql-url>' \
DATAGROUND_DEVELOPMENT_BEARER_TOKEN='<development-token-at-least-32-bytes>' \
DATAGROUND_DEVELOPMENT_PRINCIPAL_ID='usr_00000000000000000001' \
DATAGROUND_DEVELOPMENT_ISOLATION_DOMAIN_ID='iso_00000000000000000001' \
go run ./cmd/dataground-api
DATAGROUND_DATABASE_URL='<postgresql-url>' go run ./cmd/dataground-worker
```

Durable mode persists resources, idempotency results, explicit publication and invocation operations, event replay, external-effect receipts, outbox events, command audits, append-only authentication attempts, and append-only API and invocation authorization decisions. A completed authentication outcome is not released until PostgreSQL records its minimized domain-scoped result with the request correlation; successful in-domain attempts include only the platform principal identity and kind. A completed API action evaluation is not released until PostgreSQL records its principal, scope, outcome, correlation, policy-set identity, and exact policy digest. Explicit effect-time composition applies the same fail-closed rule to completed invocation Cedar evaluations while binding their actor, phase, complete durable scope, correlation, and exact policy provenance. The internal `dataground-audit-export` command can read a frozen, isolation-scoped snapshot of those two decision streams through a versioned cursor and withholds every page until its append-only export receipt commits; see [authorization audit export](authorization-audit-export.md). The separate `dataground-operator-audit-export` command applies the same frozen, receipt-bound extraction rule to the closed safe subset of operator and lifecycle audit records; see [operator audit export](operator-audit-export.md). The external-key [audit export signing](audit-export-signing.md) command authenticates either exact canonical page, and [audit export encryption](audit-export-encryption.md) protects the envelope for an identity-proven X25519 recipient. The [delivery lifecycle](audit-export-delivery.md) binds that package to the exact active [recipient trust generation](audit-export-recipient-trust.md), executes a recoverable immutable mTLS S3 transport only after PostgreSQL authorizes a short-lived signed [workload identity](audit-export-workload-identity.md) bound to the exact client certificate, then requires an exact [recipient-signed receipt](audit-export-delivery-receipts.md) under the same generation, unexpired identity and key-custody proof, and effective [proof revocation](audit-export-recipient-proof-revocation.md) set. New recipient trust activations require a current isolation-scoped [proofing authority generation](audit-export-proofing-authorities.md), both notice paths require a current [revocation authority generation](audit-export-revocation-authorities.md), and the separate [revocation importer](audit-export-revocation-acquisition.md) can retrieve either notice through authenticated digest-pinned HTTPS before atomic intake. External workload-identity and recipient-proof issuance, upstream acquisition-credential issuance and revocation, production transport certification, complete external monitoring and evidence acquisition, access, and retention remain unresolved. The development authenticator and static Cedar action policy require an explicit loopback IP listener even in durable mode. The opt-in [OIDC authentication profile](oidc-authentication.md) composes reloadable JWT verification, durable identity resolution, DPoP replay protection and deployment-selected PostgreSQL nonce enforcement, trusted request binding, PostgreSQL-coordinated admission, both durable audit layers, immutable deployment inputs, and bounded keyset refresh as one command-owned lifetime. Owner-operated commands can durably audit isolation-scoped activation, rotation, or local revocation of endpoint provider credentials, bind a supplied public-key generation to an exact provider profile or import one through a digest-pinned registry with authenticated HTTPS, activate sequential audited admission-policy generations through an atomic database cutover, and measure the exact limiter and nonce-store paths against a dedicated disposable PostgreSQL profile. The API requires the published keyset, signed configuration, and serving verifier to share that provider binding, plus one exact accepted capacity record and one externally signed [release certification](release-certification.md) for the same clean build, reviewed configuration, Cedar policy, deployment profile, and public Ed25519 trust profile before durable startup; expiry removes non-liveness readiness. It still requires an explicit loopback listener; production non-loopback binding remains blocked until reviewed TLS ingress, external provider DPoP conformance execution and incorporation of its separately signed short-lived evidence, upstream provider credential acquisition and revocation with durable audit, complete production release certification beyond that narrow slice, and policy-service deployment exist. See [API action authorization](api-authorization.md) and [durable control-plane operations](durable-control-plane.md).

After an exact service revision exists, an authorized operator can install its reviewed Cedar invocation policy with `go run ./cmd/dataground-policy-install` and the required `-isolation-domain`, `-service`, `-revision`, `-policy-set`, `-policy-file`, `-actor`, `-reason`, and `-correlation-id` flags. The command requires the current PostgreSQL schema, accepts one bounded regular policy file, validates it against the canonical invocation schema, and atomically records immutable policy bytes plus safe installation audit evidence. Its database credential is the administrative authorization boundary. Identical replay is read-only; changed policy or attribution for the same revision is rejected. This is an internal operator boundary, not public policy authoring, approval workflow, signing, distribution, or reload.

Revocation imports require the current sequential, isolation-scoped source generation in addition to the current revocation authority. Source activation validates and immutably publishes a reviewed canonical registry before recording the selected source; rotation uses a new publication path, while exact retry is read-only. The publication, activation, rotation, and withdrawal procedure is documented in [revocation notice acquisition](audit-export-revocation-acquisition.md).

The worker remains deterministic by default. [Governed development worker guidance](governed-worker.md) documents the explicit single-domain mode that composes exact policy resolution and audit with OpenShell admission, Codex runtime execution, cancellation, and artifact finalization without claiming production certification.

See [design-system guidance](design-system.md) before changing token source, component APIs, themes, density behavior, or Storybook configuration.

## Contract changes

The OpenAPI and release-manifest files are compatibility surfaces even in alpha form. Change their schema identities or semantics deliberately, update examples and checks together, and do not add public infrastructure endpoints or provider-native concepts. See [`contracts/README.md`](../../contracts/README.md) for the compatibility rules and generation workflow.
