# API action authorization

Every versioned API route passes through one injected authorization boundary after bearer authentication and isolation-domain membership validation. Authorization completes before media-type validation, idempotency lookup, request-body reads, repository access, or resource disclosure. Liveness and readiness remain outside both boundaries.

The request is deliberately closed and non-sensitive. It contains the validated principal identity and kind, one action, one resource type and path-derived identifier, and the effective isolation domain. It never contains bearer credentials, request bodies, aliases, prompts, schemas, artifact paths, provider data, or repository results.

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

An explicit denial returns the stable `ACTION_FORBIDDEN` response. Invalid policy input, evaluation diagnostics, or unavailable authorization return `AUTHORIZATION_UNAVAILABLE`; Cedar details and policy identifiers are not exposed. A missing or typed-nil authorizer prevents handler construction.

`cmd/dataground-api` composes a static development Cedar policy only after confirming an explicit loopback listener. That policy is bound to the configured development human principal and one isolation domain and can permit only the closed action/resource combinations above. It is not a production policy service, policy store, entity loader, decision-audit implementation, or dynamic reload mechanism.

Production OIDC verification, immutable production policy distribution, entity snapshots and hierarchy, decision provenance/audit, policy administration, and revocation remain deployment boundaries. The separate invocation-effect authorizer still governs admission, runtime, and cancellation immediately before consequential provider effects; entry authorization does not replace that second decision.
