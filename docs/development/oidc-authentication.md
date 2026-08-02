# OIDC authentication boundary

DataGround has an internal provider-neutral boundary for turning a verified OIDC access token into a platform principal, plus a concrete verifier for one immutable JWT access-token profile. The API package can compose reloadable verification, durable identity and replay state, DPoP request binding, pre-authentication admission, authorization, and both audit layers as one owned lifetime. The API command can opt into that assembly through one strict deployment configuration, but retains an explicit loopback listener and does not become a production endpoint.

`PinnedOIDCJWTVerifier` verifies compact JWS access tokens against a deployment-owned JWKS using the security-fixed `go-jose` 4.1.4 release. The profile pins the exact HTTPS issuer, the canonical DataGround API audience, an explicit asymmetric algorithm allowlist, clock skew, and maximum token lifetime. Tokens require protected `alg` and `kid` headers, valid `exp` and `iat` claims, one bounded subject, and a bounded duplicate-free audience set containing the API audience. Expired, not-yet-valid, excessive-lifetime, wrong-issuer, wrong-audience, unknown-key, mismatched-algorithm, malformed, and duplicate-member tokens fail as invalid credentials. Cancellation and deadlines remain distinguishable, and library or provider details are not returned.

A pinned JWKS is limited to 256 KiB and 64 public signing keys. Every key requires a unique bounded `kid`, an exact allowed `alg`, and `use` set to `sig`; private, symmetric, weak RSA, mismatched ECDSA, certificate-reference, and unknown members fail at assembly. The verifier accepts reviewed overlap between old and new keys, but it never discovers or fetches an unpinned key.

`ReloadableOIDCJWTVerifier` can atomically replace that complete pinned verifier from a deployment-supplied `OIDCJWTKeysetSource`. Every snapshot has a strictly positive generation and a finite validity no more than 24 hours ahead. Exact replay is read-only; generation rollback or conflicting reuse fails without changing the active verifier. Invalid, rollback, conflict, unavailable, and cancelled refresh outcomes are distinguishable without exposing source details. Verification holds one generation through completion, an expired snapshot fails unavailable before signature work, and a transient refresh failure does not discard a still-valid active generation. The source transfers an owned JWKS copy so transient bytes are cleared after assembly. `OIDCJWTKeysetRefreshSupervisor` gives one lifecycle exclusive ownership of bounded periodic refresh attempts, serializes them with manual refreshes, and retains a still-valid generation across safe classified failures. Its operational status excludes source errors and every issuer, key, token, and digest value. Readiness requires both a running supervisor and an unexpired generation; stopping the lifecycle or reaching snapshot expiry fails readiness closed. Refresh intervals are bounded from one second through one hour, and each source-call timeout is bounded from 100 milliseconds through one minute without exceeding its interval. A source must honor cancellation; the supervisor does not abandon blocked source goroutines or overlap them with another attempt.

`OIDCJWTKeysetFileSource` loads one strict deployment publication from an absolute canonical path. The configured path itself must be a non-empty regular file, not a symlink, no larger than 260 KiB, and not writable by group or other users. Its JSON envelope contains only a positive `sequence`, an RFC 3339 `expiresAt`, and the complete `jwks` object; duplicate or unknown members, trailing data, excessive nesting, and oversized values are rejected. Malformed publications are classified separately from transient open, stat, and read failures, and successful loads transfer owned JWKS bytes.

Publishers must create and fully write a new regular file with safe permissions, make it durable according to the deployment storage contract, and atomically rename it over the configured path without introducing a symlink. The source opens the path for every refresh and reads through that one descriptor, so a rename exposes either the previous complete generation or the next complete generation. In-place mutation is unsupported; detected metadata changes fail the attempt without replacing the active verifier. The JWKS contains public keys, but its integrity remains part of the authentication boundary.

## Operator keyset publication

`PublishOIDCJWTKeysetFile` implements the writer side of that contract, and `dataground-oidc-keyset` exposes it as an owner-operated command. The command accepts one owner-only, closed, versioned request file and one bounded non-writable JWKS input. It rejects private or symmetric keys, unsupported algorithms, duplicate key identifiers, expired or excessively long generations, sequence rollback, and conflicting reuse. Supported public keys are canonicalized by key identifier so semantically identical input does not create formatting-only conflicts.

The publisher serializes cooperating processes through an adjacent advisory lock, writes a mode-`0600` temporary file in the target directory, syncs the file, verifies that the target did not change, atomically renames it, and syncs the directory. Exact replay does not rewrite the generation and re-syncs the directory, so retry can resolve an uncertain directory-sync result safely. The target directory must be an absolute non-symlink path not writable by group or other users. All writers for one target must use this command; an unrelated writer that ignores the advisory lock is outside the contract.

The request has this closed shape; replace the expiry placeholder with an RFC 3339 UTC time after the current time and no more than 24 hours ahead:

```json
{
  "contract": "dataground.oidc-keyset-publication/v1",
  "sequence": 2,
  "expiresAt": "REPLACE_WITH_BOUNDED_RFC3339_UTC_EXPIRY",
  "algorithms": ["EdDSA"],
  "jwksFile": "/run/dataground/provider-jwks.json",
  "publicationFile": "/etc/dataground/oidc-keysets.json"
}
```

Create the request with mode `0600`, ensure the JWKS and publication directory satisfy the permission contract, then run `go run ./cmd/dataground-oidc-keyset -request-file /run/dataground/keyset-request.json`. The command emits no key document. It never generates, retrieves, or stores provider private keys and does not choose a provider or revocation policy. Deployments can supply a reviewed public JWKS directly or use the pinned OIDC discovery import below.

## OIDC discovery keyset import

`OIDCDiscoveryKeysetImporter` retrieves a standard public provider JWKS only after pinning the exact issuer, discovery URL, JWKS URL, and asymmetric algorithm allowlist. It verifies that discovery metadata repeats the pinned issuer and JWKS endpoint exactly, refuses every redirect, requires HTTPS with TLS 1.2 or stronger, bounds response headers and bodies, disables implicit compression, and accepts only JSON media types appropriate to each endpoint. Discovery metadata may contain standard extension members, but duplicate members, excessive nesting, malformed JSON, an endpoint change, or an issuer change fails the import. The imported JWKS passes the same strict public-key validation and canonicalization as a locally supplied generation.

`dataground-oidc-keyset-import` combines that authenticated acquisition with the existing monotonic atomic publisher. The owner-only request pins all network and publication inputs:

```json
{
  "contract": "dataground.oidc-keyset-import/oidc-discovery/v1",
  "issuer": "https://identity.example.invalid/realms/dataground",
  "discoveryUrl": "https://identity.example.invalid/realms/dataground/.well-known/openid-configuration",
  "jwksUrl": "https://identity.example.invalid/realms/dataground/protocol/openid-connect/certs",
  "sequence": 2,
  "expiresAt": "REPLACE_WITH_BOUNDED_RFC3339_UTC_EXPIRY",
  "algorithms": ["EdDSA"],
  "publicationFile": "/etc/dataground/oidc-keysets.json"
}
```

Create the request with mode `0600`, select a positive sequence greater than the installed generation, and choose a publication expiry after the current time and no more than 24 hours ahead. Run `go run ./cmd/dataground-oidc-keyset-import -request-file /run/dataground/keyset-import-request.json`. The command uses the process trust store, follows no redirects, writes no intermediate JWKS, and emits no key document. Provider selection, private-key custody, revocation timing, authentication for non-public metadata endpoints, and retry scheduling remain deployment responsibilities.

The verified token cannot supply a DataGround principal identifier, principal kind, roles, groups, or isolation-domain membership. The PostgreSQL registry resolves only the exact issuer and subject. Each immutable registration grants one human or external-service principal membership in one isolation domain; active rows for the same external identity must agree on principal identity and kind. Resolution assembles a deterministic owned membership set from non-revoked registrations and fails closed on malformed or inconsistent data. Internal platform, sandbox, and distributed-compute identities remain outside OIDC and require the workload-identity and mTLS boundary from ADR-016.

Registration and revocation are separate append-only facts. The `dataground-oidc-identity` operator command accepts one owner-only JSON request file no larger than 64 KiB, requires the current PostgreSQL schema, and atomically records the registry fact and a scope-local audit record. Identical replay is read-only; changed attribution, principal data, correlation, or reason under the same domain/issuer/subject scope conflicts. Revocation never deletes or rewrites registration evidence, cannot reactivate a subject, and removes only its exact isolation-domain membership from subsequent resolutions. The database credential is the administrative authorization boundary.

A registration request has this closed shape:

```json
{
  "operation": "register",
  "isolationDomainId": "iso_00000000000000000001",
  "issuer": "https://identity.example.invalid/realms/dataground",
  "subject": "provider-owned-subject",
  "principalId": "usr_00000000000000000001",
  "principalKind": "human",
  "actorId": "usr_00000000000000000002",
  "reason": "approved initial registration",
  "correlationId": "cor_00000000000000000001"
}
```

Revocation uses the same shape with `operation` set to `revoke` and omits `principalKind`; the supplied principal identifier must match the existing binding. Create the file with mode `0600`, run `DATAGROUND_DATABASE_URL=... go run ./cmd/dataground-oidc-identity -request-file ./request.json`, then remove it using the installation's approved sensitive-file procedure. The command emits no identity document. Raw issuer and subject remain in the registry because they are lookup keys, while audit metadata contains only deterministic identity and binding digests, platform principal data, and the reason digest.

PostgreSQL-backed durable API mode now wraps its configured authenticator in a fail-closed attempt-audit boundary. Each completed attempt records only its path-derived isolation domain, generated request correlation, credential method, and coarse outcome. Successful in-domain attempts additionally record the platform principal identifier and kind; rejected, unavailable, and cross-domain attempts contain no principal data. Tokens, token digests, issuer, subject, audience, key ID, request path, client address, and dependency diagnostics are never part of this record. The executable uses the same durable audit boundary for its opt-in loopback OIDC/DPoP profile; the static bearer authenticator remains the default development mode.

`AuthenticationRateLimiter` is a deployment-owned admission boundary used only by the explicit rate-limited DPoP handler constructors. It receives an immutable request with accessors for a validated isolation-domain identifier and copied SHA-256 credential digest, never the raw bearer token, proof, request body, host, proxy headers, or client address. The request has no exported fields, so generic serialization does not disclose the digest. Admission runs before DPoP parsing, token verification, authorization, and request-body reads. A bounded denial returns a stable retryable `429` with an integer `Retry-After`; limiter failure or an invalid decision fails closed with a retryable `503`. Credentials are consumed on every admission exit. Health probes stay outside this path, and rate-limit outcomes do not become authentication-attempt audit records because authentication has not started.

`PostgreSQLAuthenticationRateLimiter` supplies one explicit shared-window policy with monotonically narrower global, isolation-domain, and credential bursts. It uses PostgreSQL's clock and serialized global admission to coordinate every process sharing the repository. Only domain-separated subject digests are stored; every process pins one operator-activated generation and its canonical policy digest, and stale or conflicting configuration fails closed. Global admission bounds the number of attacker-selected domain and credential buckets that can be created, while bounded opportunistic reclamation removes fully refilled non-global state. The implementation has no process-local fallback. Deployments still own policy selection, capacity validation, trusted network-edge controls, and observability without subject digests.


## Admission policy activation

`dataground-auth-rate-limit-policy` activates one reviewed policy generation before the matching API processes become ready. It accepts one owner-only, closed request file and requires the current PostgreSQL schema. The database credential is the administrative authorization boundary.

```json
{
  "contract": "dataground.authentication-rate-limit-policy/v1",
  "generation": 1,
  "window": "1m",
  "globalBurst": 100,
  "isolationDomainBurst": 20,
  "credentialBurst": 10,
  "actorId": "usr_00000000000000000001",
  "reason": "reviewed initial admission policy",
  "correlationId": "cor_00000000000000000001"
}
```

Create the request at an absolute canonical path with mode `0600`, migrate the database, then run `DATAGROUND_DATABASE_URL=... go run ./cmd/dataground-auth-rate-limit-policy -request-file /run/dataground/admission-policy.json`. The first generation must be 1 and every replacement must increment exactly by one. Exact replay of the active generation is read-only; rollback, gaps, conflicting reuse, and reuse of a historical generation fail closed.

Activation takes the exclusive side of the same database advisory lock held in shared mode by every admission. It waits for in-flight admissions, appends operator attribution and a reason digest, clears only the previous generation's transient buckets, and commits the new generation atomically. The cutover intentionally grants one fresh burst under the newly reviewed policy. Processes still configured for the previous generation immediately fail admission and readiness, while staged processes become ready only when their exact generation and values are active. Production values still require deployment-specific load and abuse testing; the repository does not infer them.

`ReloadableOIDCDPoPAuthenticator` owns one reloadable keyset verifier, DPoP verifier, replay store, and identity resolver. `DurableOIDCDPoPAssembly` binds that chain to one trusted external origin, deployment rate limiter, durable API repository, audited authenticator, and audited authorizer. The assembly exposes only its HTTP handler, the exact serving verifier's refresh lifecycle, minimized refresh status, and readiness, so callers cannot supervise or rotate a verifier that is not serving the handler. Every route except liveness fails closed and consumes credential headers unless the refresh lifecycle owns a still-valid generation. Static HTTP and DPoP profile errors fail before the keyset source is contacted.

Provider selection, provider revocation policy, authenticated non-public metadata endpoints, group-to-membership administration, HTTPS ingress deployment, provider-side DPoP issuance, optional nonce policy, measured admission capacity, workload identity, and production conformance remain unimplemented. The opt-in OIDC profile and default static development identity both require explicit loopback listeners and must not bind publicly.


## Loopback executable activation

Set `DATAGROUND_API_SECURITY_CONFIG_FILE` to the absolute canonical path of one non-empty regular JSON file no larger than 64 KiB. The configuration file and referenced Cedar policy must not be symlinks or writable by group or other users, and startup verifies that each file remains the same size, mode, and identity across its bounded read. OIDC mode rejects a simultaneously configured development bearer credential and requires `DATAGROUND_DATABASE_URL` at the current schema.

```json
{
  "contract": "dataground.api-security/oidc-dpop/v1",
  "issuer": "https://identity.example.invalid/realms/dataground",
  "externalOrigin": "https://api.example.invalid",
  "keysetPublicationFile": "/etc/dataground/oidc-keysets.json",
  "algorithms": ["EdDSA"],
  "jwt": {
    "clockSkew": "30s",
    "maximumLifetime": "1h"
  },
  "dpop": {
    "clockSkew": "30s",
    "maximumProofAge": "1m"
  },
  "keysetRefresh": {
    "interval": "1m",
    "timeout": "5s"
  },
  "admission": {
    "generation": 1,
    "window": "1m",
    "globalBurst": 100,
    "isolationDomainBurst": 20,
    "credentialBurst": 10
  },
  "authorization": {
    "policySetId": "deployment-api-v1",
    "policyFile": "/etc/dataground/api.cedar"
  }
}
```

Start the opt-in profile with the migrated durable database and explicit configuration path:

```shell
DATAGROUND_DATABASE_URL='postgres://dataground:dataground@127.0.0.1:5432/dataground?sslmode=disable' \\
DATAGROUND_API_SECURITY_CONFIG_FILE='/etc/dataground/api-security.json' \\
go run ./cmd/dataground-api
```

The values above describe the contract shape, not recommended production capacity. Deployments must select and validate the complete JWT, DPoP, refresh, admission, and Cedar profile. The keyset publication uses the separate atomic envelope and owner-operated publisher described above. Startup reads and validates the closed configuration, filesystem boundaries, Cedar policy, external origin, refresh policy, and admission policy before contacting PostgreSQL. It then completes the cryptographic profile assembly against the current durable repository and loads the initial keyset. The refresh lifecycle and HTTP server share cancellation: an unexpected lifecycle exit shuts the server down, while stopped or expired refresh ownership makes every non-liveness route unavailable and consumes bearer and DPoP headers. Public binding remains prohibited.

## DPoP request binding

The internal `DPoPTokenVerifier` implements the protected-resource proof checks from [RFC 9449](https://www.rfc-editor.org/rfc/rfc9449.html). It wraps an already cryptographically verified access token, requires that token to carry a valid `cnf.jkt`, and accepts only ES256 or EdDSA proofs with a public embedded JWK. Every proof is bound to one uppercase HTTP method, one trusted canonical HTTPS external URI without query or fragment, its access token through `ath`, a bounded creation window, and a unique proof identifier. Missing, malformed, stale, future, wrong-method, wrong-URI, wrong-token, wrong-key, or replayed proofs fail as invalid credentials.

The optional API request binder supplies that trusted HTTP composition. It accepts one pinned canonical HTTPS origin at assembly, combines it only with the routed request's canonical escaped path, and excludes the query as required by RFC 9449. Caller-controlled host, `Forwarded`, and `X-Forwarded-*` values cannot change the proof URI. Absolute-form targets, dot segments, duplicate separators, non-canonical escaping, duplicate or oversized proof headers, and ambiguous origin forms fail before token verification. The binder removes the DPoP header from the request before authentication continues.

Before a verified token can reach identity resolution, PostgreSQL reserves SHA-256 digests of the JWK thumbprint and proof identifier in one exact isolation domain. The raw proof, access token, token digest, public key, method, URI, issuer, and subject are not persisted. Active reservations are immutable and cannot be deleted; bounded domain-scoped cleanup can reclaim expired rows.

This is not production API activation. A deployment still needs an authorization server that issues DPoP-bound access tokens, reviewed TLS ingress that routes the configured origin without rewriting its canonical path, optional nonce policy, provider selection and authenticated non-public metadata endpoints where required, deployment rate-limit policy rollout and measured capacity, and reviewed non-loopback deployment activation. The executable's OIDC profile therefore remains loopback-only.
