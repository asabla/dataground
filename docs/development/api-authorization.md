# API action authorization

Every versioned API route passes through one injected authorization boundary after bearer authentication and isolation-domain membership validation. Authorization completes before media-type validation, idempotency lookup, request-body reads, repository access, or resource disclosure. Liveness and readiness remain outside both boundaries.

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
| Read invocation events | `readInvocationEvents` | `Invocation` from `invocationId` |
| Read invocation artifact | `readInvocationArtifact` | `Artifact` from `artifactId` |

An explicit denial returns the stable `ACTION_FORBIDDEN` response. Invalid policy input, evaluation diagnostics, unavailable authorization, or a failed required audit write return `AUTHORIZATION_UNAVAILABLE`; Cedar details and policy identifiers are not exposed. A missing or typed-nil authorizer prevents handler construction.

`cmd/dataground-api` composes a static development Cedar policy only after confirming an explicit loopback listener. That policy is bound to the configured development human principal and one isolation domain and can permit only the closed action/resource combinations above. It is not a production policy service, policy store, entity loader, or dynamic reload mechanism.

When PostgreSQL durable mode is selected, startup also requires an audited-authorizer composition. Every completed allow, deny, or unavailable evaluation is inserted before its result is released. The append-only row records only the validated principal identity and kind, isolation domain, closed action and resource identity, stable outcome, request correlation, policy-set identifier, and SHA-256 digest of the length-delimited canonical schema and policy bytes. Database time and a generated sequence order the records. The table rejects update and delete operations, and the application recorder has no read or mutation method. Tokens, request bodies, policy bytes, Cedar diagnostics, aliases, prompts, schemas, artifact paths, provider data, and repository results are excluded. Process-local mode remains ephemeral and does not claim durable decision auditing.

Production OIDC verification, immutable production policy distribution, entity snapshots and hierarchy, audit export, retention and access policy, policy administration, and revocation remain deployment boundaries. The separate invocation-effect authorizer still governs admission, runtime, and cancellation immediately before consequential provider effects; entry authorization does not replace that second decision.
