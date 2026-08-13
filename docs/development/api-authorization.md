# API action authorization

Every versioned API route passes through one injected authorization boundary after credential authentication and isolation-domain membership validation. The default development profile uses Bearer credentials; the opt-in OIDC profile requires the DPoP authorization scheme and proof. Authorization completes before media-type validation, idempotency lookup, request-body reads, repository access, or resource disclosure. Liveness and readiness remain outside both boundaries.

The request is deliberately closed and non-sensitive. It contains the validated principal identity and kind, one action, one resource type and path-derived identifier, the effective isolation domain, and an opaque request correlation identifier. It never contains bearer credentials, request bodies, aliases, prompts, schemas, artifact paths, provider data, or repository results.

| API operation | Cedar action | Cedar resource |
| --- | --- | --- |
| Create agent service | `createAgentService` | `IsolationDomain` from `isolationDomainId` |
| Create service revision | `createServiceRevision` | `AgentService` from `serviceId` |
| Publish service revision | `publishServiceRevision` | `ServiceRevision` from `revisionId` |
| Assign service alias | `assignServiceAlias` | `AgentService` from `serviceId` |
| Invoke agent service | `invokeAgentService` | `AgentService` from `serviceId` |
| Read invocation | `readInvocation` | `Invocation` from `invocationId` |
| Read operation | `readOperation` | `Operation` from `operationId` |
| Cancel invocation | `cancelInvocation` | `Invocation` from `invocationId` |
| Read invocation approval | `readInvocationApproval` | `InvocationApproval` from `approvalId` |
| Resolve invocation approval | `resolveInvocationApproval` | `InvocationApproval` from `approvalId` |
| Read invocation events | `readInvocationEvents` | `Invocation` from `invocationId` |
| Read invocation artifact | `readInvocationArtifact` | `Artifact` from `artifactId` |

An explicit denial returns the stable `ACTION_FORBIDDEN` response. Invalid policy input, evaluation diagnostics, unavailable authorization, or a failed required audit write return `AUTHORIZATION_UNAVAILABLE`; Cedar details and policy identifiers are not exposed. A missing or typed-nil authorizer prevents handler construction.

`cmd/dataground-api` composes a static development Cedar policy only after confirming an explicit loopback listener. That policy is bound to the configured development human principal and one isolation domain and can permit only the closed action/resource combinations above. It is not a production policy service, policy store, entity loader, or dynamic reload mechanism.

When PostgreSQL durable mode is selected, startup also requires an audited-authorizer composition. Every completed allow, deny, or unavailable evaluation is inserted before its result is released. The append-only row records only the validated principal identity and kind, isolation domain, closed action and resource identity, stable outcome, request correlation, policy-set identifier, and SHA-256 digest of the length-delimited canonical schema and policy bytes. Database time and a generated sequence order the records. The table rejects update and delete operations, and the application recorder has no read or mutation method. Tokens, request bodies, policy bytes, Cedar diagnostics, aliases, prompts, schemas, artifact paths, provider data, and repository results are excluded. Process-local mode remains ephemeral and does not claim durable decision auditing.

The internal [authorization audit export](authorization-audit-export.md) command can extract frozen, isolation-scoped pages from the API and invocation decision tables only after recording the exact request and content digests in an append-only receipt. Exact signed pages can be [encrypted](audit-export-encryption.md) to an identity-proven X25519 recipient, prepared for a deployment-owned destination under that same active [recipient trust generation](audit-export-recipient-trust.md), transported through the bound immutable mTLS S3 profile only under an active signed [workload identity](audit-export-workload-identity.md), and acknowledged only through an exact [recipient-signed receipt](audit-export-delivery-receipts.md) without a matching effective [proof revocation](audit-export-recipient-proof-revocation.md) in the separate [delivery lifecycle](audit-export-delivery.md). Either revocation-notice type can be acquired through the authenticated digest-pinned [revocation importer](audit-export-revocation-acquisition.md). Production OIDC verification, immutable production policy distribution, entity snapshots and hierarchy, external audit-transport workload-identity and recipient-proof issuance, acquisition-credential issuance and remote revocation, complete external evidence monitoring, audit-transport production certification, retention and access policy, and policy administration remain deployment boundaries. The separate invocation-effect authorizer still governs admission, runtime, and cancellation immediately before consequential provider effects; entry authorization does not replace that second decision.
