# OIDC authentication boundary

DataGround has an internal provider-neutral boundary for turning a verified OIDC access token into a platform principal, plus a concrete verifier for one immutable JWT access-token profile. The API package can compose reloadable verification, durable identity, proof replay and optional resource-server nonce state, DPoP request binding, pre-authentication admission, authorization, and both audit layers as one owned lifetime. The API command can opt into that assembly through one strict deployment configuration, but retains an explicit loopback listener and does not become a production endpoint.

`PinnedOIDCJWTVerifier` verifies compact JWS access tokens against a deployment-owned JWKS using the security-fixed `go-jose` 4.1.4 release. The profile pins the exact HTTPS issuer, the canonical DataGround API audience, an explicit asymmetric algorithm allowlist, clock skew, and maximum token lifetime. Tokens require protected `alg` and `kid` headers, valid `exp` and `iat` claims, one bounded subject, and a bounded duplicate-free audience set containing the API audience. Expired, not-yet-valid, excessive-lifetime, wrong-issuer, wrong-audience, unknown-key, mismatched-algorithm, malformed, and duplicate-member tokens fail as invalid credentials. Cancellation and deadlines remain distinguishable, and library or provider details are not returned.

A pinned JWKS is limited to 256 KiB and 64 public signing keys. Every key requires a unique bounded `kid`, an exact allowed `alg`, and `use` set to `sig`; private, symmetric, weak RSA, mismatched ECDSA, certificate-reference, and unknown members fail at assembly. The verifier accepts reviewed overlap between old and new keys, but it never discovers or fetches an unpinned key.

`ReloadableOIDCJWTVerifier` can atomically replace that complete pinned verifier from a deployment-supplied `OIDCJWTKeysetSource`. Every snapshot has a strictly positive generation, the exact provider-profile identity and registry digest selected by the signed serving configuration, and a finite validity no more than 24 hours ahead. Exact replay is read-only; provider drift, generation rollback, or conflicting reuse fails without changing the active verifier. Invalid, rollback, conflict, unavailable, and cancelled refresh outcomes are distinguishable without exposing source details. Verification holds one generation through completion, an expired snapshot fails unavailable before signature work, and a transient refresh failure does not discard a still-valid active generation. The source transfers an owned JWKS copy so transient bytes are cleared after assembly. `OIDCJWTKeysetRefreshSupervisor` gives one lifecycle exclusive ownership of bounded periodic refresh attempts, serializes them with manual refreshes, and retains a still-valid generation across safe classified failures. Its operational status excludes source errors and every issuer, provider, key, token, and digest value. Readiness requires both a running supervisor and an unexpired generation; stopping the lifecycle or reaching snapshot expiry fails readiness closed. Refresh intervals are bounded from one second through one hour, and each source-call timeout is bounded from 100 milliseconds through one minute without exceeding its interval. A source must honor cancellation; the supervisor does not abandon blocked source goroutines or overlap them with another attempt.

`OIDCJWTKeysetFileSource` loads one strict v2 deployment publication from an absolute canonical path. The configured path itself must be a non-empty regular file, not a symlink, no larger than 260 KiB, and not writable by group or other users. Its JSON envelope contains its contract, a positive `sequence`, the selected `providerId` and lowercase `providerRegistrySha256`, an RFC 3339 `expiresAt`, and the complete `jwks` object; duplicate or unknown members, trailing data, excessive nesting, and oversized values are rejected. Malformed publications are classified separately from transient open, stat, and read failures, and successful loads transfer owned JWKS bytes.

Publishers must create and fully write a new regular file with safe permissions, make it durable according to the deployment storage contract, and atomically rename it over the configured path without introducing a symlink. The source opens the path for every refresh and reads through that one descriptor, so a rename exposes either the previous complete generation or the next complete generation. In-place mutation is unsupported; detected metadata changes fail the attempt without replacing the active verifier. The JWKS contains public keys, but its integrity remains part of the authentication boundary.

## Operator keyset publication

`PublishOIDCJWTKeysetFile` implements the writer side of that contract, and `dataground-oidc-keyset` exposes it as an owner-operated command. The command accepts one owner-only, closed, versioned request file and one bounded non-writable JWKS input. It rejects private or symmetric keys, unsupported algorithms, duplicate key identifiers, expired or excessively long generations, sequence rollback, and conflicting reuse. Supported public keys are canonicalized by key identifier so semantically identical input does not create formatting-only conflicts.

The publisher serializes cooperating processes through an adjacent advisory lock, writes a mode-`0600` temporary file in the target directory, syncs the file, verifies that the target did not change, atomically renames it, and syncs the directory. Exact replay does not rewrite the generation and re-syncs the directory, so retry can resolve an uncertain directory-sync result safely. The target directory must be an absolute non-symlink path not writable by group or other users. All writers for one target must use this command; an unrelated writer that ignores the advisory lock is outside the contract.

The request has this closed shape; replace the expiry placeholder with an RFC 3339 UTC time after the current time and no more than 24 hours ahead:

```json
{
  "contract": "dataground.oidc-keyset-publication-request/v2",
  "sequence": 2,
  "providerId": "primary",
  "providerRegistrySha256": "REPLACE_WITH_LOWERCASE_SHA256",
  "expiresAt": "REPLACE_WITH_BOUNDED_RFC3339_UTC_EXPIRY",
  "algorithms": ["EdDSA"],
  "jwksFile": "/run/dataground/provider-jwks.json",
  "publicationFile": "/etc/dataground/oidc-keysets.json"
}
```

Create the request with mode `0600`, bind the reviewed public JWKS to its selected provider-profile identity and exact registry digest, ensure the JWKS and publication directory satisfy the permission contract, then run `go run ./cmd/dataground-oidc-keyset -request-file /run/dataground/keyset-request.json`. The command emits no key document. It never generates, retrieves, or stores provider private keys and does not independently verify how a directly supplied JWKS was acquired. Deployments can supply reviewed public keys through that explicit assertion boundary or use the registry-validated discovery import below.

## OIDC discovery keyset import

`OIDCDiscoveryKeysetImporter` retrieves a standard public provider JWKS only after pinning the exact issuer, discovery URL, JWKS URL, and asymmetric algorithm allowlist. It verifies that discovery metadata repeats the pinned issuer and JWKS endpoint exactly, refuses every redirect, requires HTTPS with TLS 1.2 or stronger, bounds response headers and bodies, disables implicit compression, and accepts only JSON media types appropriate to each endpoint. Discovery metadata may contain standard extension members, but duplicate members, excessive nesting, malformed JSON, an endpoint change, or an issuer change fails the import. The imported JWKS passes the same strict public-key validation and canonicalization as a locally supplied generation.

`dataground-oidc-keyset-import` combines that authenticated acquisition with the existing monotonic atomic publisher. Network and algorithm inputs come only from one selected profile in a closed deployment registry. Endpoint bearer credentials are separate monotonic publications managed by `dataground-oidc-provider-credential`; the registry cannot name a raw token file. This example keeps discovery public and authenticates the non-public JWKS endpoint with one endpoint-scoped publication:

```json
{
  "contract": "dataground.oidc-provider-registry/v2",
  "profiles": [{
    "id": "primary",
    "issuer": "https://identity.example.invalid/realms/dataground",
    "discoveryUrl": "https://identity.example.invalid/realms/dataground/.well-known/openid-configuration",
    "jwksUrl": "https://identity.example.invalid/realms/dataground/protocol/openid-connect/certs",
    "algorithms": ["EdDSA"],
    "discoveryAuthentication": {"kind": "none"},
    "jwksAuthentication": {"kind": "bearer-credential-file", "credentialFile": "/var/lib/dataground/oidc-credentials/primary-jwks.json"}
  }]
}
```

The owner-only request selects exactly one registered profile and pins the SHA-256 digest of the registry's exact bytes:

```json
{
  "contract": "dataground.oidc-keyset-import/oidc-discovery/v4",
  "isolationDomainId": "iso_00000000000000000001",
  "providerId": "primary",
  "providerRegistryFile": "/etc/dataground/oidc-providers.json",
  "providerRegistrySha256": "REPLACE_WITH_LOWERCASE_SHA256",
  "sequence": 2,
  "expiresAt": "REPLACE_WITH_BOUNDED_RFC3339_UTC_EXPIRY",
  "publicationFile": "/etc/dataground/oidc-keysets.json"
}
```

Create the registry first with its final canonical credential-publication paths, then compute its exact SHA-256 digest. Each authenticated endpoint requires a distinct publication bound to that provider identity, registry digest, and endpoint; one file cannot authorize both discovery and JWKS access. `none` must omit `credentialFile`, while `bearer-credential-file` requires it. Explicit `null`, legacy raw-token profiles, duplicate profiles or algorithms, unknown profiles, registry drift, revoked or expired credentials, redirects, and authentication fallback fail closed.

Activate or rotate a local endpoint credential from a fresh owner-only token file with this closed request. The activation time may be at most five minutes ahead, the expiry must be in the future and after activation, and one generation may span no more than 31 days:

```json
{
  "contract": "dataground.oidc-provider-credential-request/v2",
  "operation": "activate",
  "isolationDomainId": "iso_00000000000000000001",
  "generation": 1,
  "providerId": "primary",
  "providerRegistrySha256": "REPLACE_WITH_LOWERCASE_SHA256",
  "endpoint": "jwks",
  "activatedAt": "REPLACE_WITH_RFC3339_UTC_TIME",
  "expiresAt": "REPLACE_WITH_BOUNDED_RFC3339_UTC_EXPIRY",
  "bearerTokenFile": "/run/dataground/provider-jwks.token",
  "publicationFile": "/var/lib/dataground/oidc-credentials/primary-jwks.json",
  "actorId": "operator_one",
  "reason": "activate the reviewed provider credential",
  "correlationId": "cor_00000000000000000001"
}
```

The request, token file, publication directory, and installed credential are owner-only and free of symlinks. The token file contains one bounded RFC 6750 bearer token without whitespace or a trailing newline. Migrate the audit database, set `DATAGROUND_DATABASE_URL`, and run `go run ./cmd/dataground-oidc-provider-credential -request-file /run/dataground/provider-credential.json`, then remove the input token through the installation's approved sensitive-file procedure. Activation starts at generation 1; rotation increments exactly by one. Exact replay is read-only, while rollback, gaps, conflicting reuse, cross-domain use, binding drift, and unsafe filesystem replacement fail closed. The command first reserves an exact isolation-scoped, operator-attributed operation in PostgreSQL, then holds the verified directory by descriptor, installs a mode-`0600` canonical publication through same-directory atomic rename, synchronizes the directory, and atomically marks the operation successful with an append-only audit record. A `prepared` row means the filesystem outcome is absent or unresolved; recovery repeats the exact owner-only request. The confidential operation row binds the credential digest but the audit record, output, and errors never expose credential bytes or that digest.

Local revocation uses the next generation with `operation` set to `revoke`, includes a `revokedAt` no more than five minutes from the command clock, and omits `activatedAt`, `expiresAt`, and `bearerTokenFile`. It retains the isolation domain, actor, reason, and correlation fields and installs a timestamped durable tombstone instead of deleting the prior state; a later locally acquired credential may be activated only at the following generation. A publication path remains permanently scoped to its first isolation domain, provider, and endpoint, while a new registry digest may be adopted only through the next generation. This boundary audits DataGround's local credential use but does not acquire a credential from the provider or revoke it at the provider. The internal [operator audit export](operator-audit-export.md) can extract the safe provider-binding, generation, endpoint, attribution, and reason-digest fields through a frozen receipt-bound page without credential bytes or credential digests. Exact signed pages can be [encrypted](audit-export-encryption.md) to an identity-proven X25519 recipient, associated with a deployment-owned destination under that same active [recipient trust generation](audit-export-recipient-trust.md), and acknowledged through an exact [recipient-signed receipt](audit-export-delivery-receipts.md) without a matching effective [proof revocation](audit-export-recipient-proof-revocation.md), but provider-side issuance, remote revocation, custody before installation, audit transport, proofing- and revocation-authority governance, external notice acquisition, access, and retention remain deployment responsibilities.

After the required credential publications are active, create the import request with mode `0600`, select a positive keyset sequence greater than the installed generation, choose a keyset expiry after the current time and no more than 24 hours ahead, and run `go run ./cmd/dataground-oidc-keyset-import -request-file /run/dataground/keyset-import-request.json`. The import command uses the process trust store, writes no intermediate JWKS, emits no key or credential document, consumes copied credential bytes after one attempt, and carries the selected provider binding into the published keyset generation. Private-key custody, provider-side credential acquisition and revocation, retry scheduling, and broader workload-identity authentication remain deployment responsibilities.

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

`AuthenticationRateLimiter` is a deployment-owned admission boundary used only by the explicit rate-limited DPoP handler constructors. It receives an immutable request with accessors for a validated isolation-domain identifier and copied SHA-256 access-token digest, never the raw token, proof, request body, host, proxy headers, or client address. The request has no exported fields, so generic serialization does not disclose the digest. Admission runs before DPoP parsing, token verification, authorization, and request-body reads. A bounded denial returns a stable retryable `429` with an integer `Retry-After`; limiter failure or an invalid decision fails closed with a retryable `503`. Credentials are consumed on every admission exit. Health probes stay outside this path, and rate-limit outcomes do not become authentication-attempt audit records because authentication has not started.

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

Activation takes the exclusive side of the same database advisory lock held in shared mode by every admission. It waits for in-flight admissions, appends operator attribution and a reason digest, and commits the new generation atomically. Bucket keys include their generation, so cutover work does not scale with historical state; subsequent admitted requests reclaim at most 128 retired or fully refilled rows per transaction. The cutover intentionally grants one fresh burst under the newly reviewed policy. Processes still configured for the previous generation immediately fail admission and readiness, while staged processes become ready only when their exact generation and values are active. Production values still require deployment-specific load and abuse testing; the repository does not infer them.

## Admission capacity evidence

`dataground-auth-rate-limit-capacity` measures the exact PostgreSQL admission implementation under credential, isolation-domain, and global contention and, when selected, the exact DPoP nonce store under shared-key issuance, distinct-key issuance, and valid-nonce lookup. It runs only against a migrated disposable database whose exact name matches the request and whose admission activation, bucket, and nonce tables are empty. The command activates three fresh generations with the candidate policy, never truncates state, and refuses database reuse. Do not point it at a development, staging, or production control-plane database.

The owner-only request fixes the deployment profile, candidate policy, load shape, acceptance thresholds, and run deadline without inventing capacity targets:

```json
{
  "contract": "dataground.authentication-rate-limit-capacity/v3",
  "runId": "cap_0123456789abcdefghij",
  "sourceRevision": "0123456789abcdef0123456789abcdef01234567",
  "deploymentProfile": "team",
  "databaseName": "dataground_auth_capacity",
  "window": "1m",
  "globalBurst": 100,
  "isolationDomainBurst": 20,
  "credentialBurst": 10,
  "attemptsPerPhase": 200,
  "workers": 20,
  "maximumP99Latency": "100ms",
  "minimumThroughputPerSecond": 50,
  "dpopNonce": {
    "enabled": true,
    "lifetime": "1m",
    "maximumActivePerKey": 4,
    "attemptsPerPhase": 200,
    "workers": 20,
    "maximumP99Latency": "100ms",
    "minimumThroughputPerSecond": 50
  },
  "maximumRunDuration": "10m"
}
```

Replace `sourceRevision` with the full commit SHA being certified, set the admission attempts above `globalBurst`, and make the nonce attempts exceed `maximumActivePerKey`, then create the request with mode `0600` at an absolute canonical path. A deployment that does not select nonce enforcement must still state `"dpopNonce":{"enabled":false}`; `null`, omission, and dormant sizing fields are rejected. The command rejects dirty or mismatched build metadata before database access. Create and migrate a dedicated database with `capacity` in its name, then run `DATAGROUND_DATABASE_URL=... go run ./cmd/dataground-auth-rate-limit-capacity -request-file /run/dataground/admission-capacity.json -output-file /var/lib/dataground/evidence/admission-capacity.json`. The output directory must already exist, must not be a symlink or writable by group or other users, and the output file must not exist. The probe gives its PostgreSQL pool the larger of the two bounded worker counts. Cancellation or dependency failure produces no artifact. Admission acceptance requires every phase to exercise denial and remain within the GCRA burst-and-refill bound. Nonce acceptance requires all issuance decisions to challenge, all lookup decisions to validate, each phase to finish within the selected nonce lifetime, and the active rows to match the configured overlap or generated key count. Every phase must also meet its declared p99 and throughput thresholds. A completed threshold or semantic miss writes the canonical record with `accepted` set to `false` and exits unsuccessfully, preserving measurements without making it eligible for deployment.

Treat the database as consumed after the command begins, even when a later phase or evidence installation fails. Preserve it for diagnosis and start any retry with a newly created, empty database and a new run identifier. If output installation reports an error, inspect the requested target and adjacent `.dataground-auth-capacity-*` file before cleanup because the durable link may have succeeded before directory synchronization became uncertain.

The canonical evidence records the exact source revision, Go runtime, policy digest, PostgreSQL server version and connection ceiling, both fixed load shapes, the selected nonce lifetime and overlap, aggregate decisions and active-row counts, integer latency percentiles, throughput, and threshold outcomes. Synthetic isolation domains, credential material, DPoP key digests, and nonce values are never included. One successful record covers only its exact database, policies, worker counts, attempt counts, thresholds, and deployment profile; it is an input to a release certification manifest, not a universal capacity claim.

The opt-in API profile incorporates one record only when its owner-only path and lowercase SHA-256 digest are pinned in the immutable security configuration. Startup strictly revalidates every evidence member and phase, requires `accepted: true`, and binds the record to the exact clean executable revision, Go runtime, deployment profile, configured admission policy, and configured nonce policy before PostgreSQL access. Readiness then continuously requires the serving database's PostgreSQL version and `max_connections` ceiling to match the measured profile, in addition to the active policy-generation check. File pinning is deployment-local evidence incorporation, not a signature, infrastructure certification, or repository release-manifest endorsement; changes to the executable, Go runtime, either policy, deployment profile, load shape, threshold, server version, connection ceiling, or evidence bytes require a new accepted run and configuration digest.

`ReloadableOIDCDPoPAuthenticator` owns one reloadable keyset verifier, DPoP verifier, proof replay store, optional nonce store, and identity resolver. `DurableOIDCDPoPAssembly` binds that chain to one trusted external origin, deployment rate limiter, durable API repository, audited authenticator, and audited authorizer. The assembly exposes only its HTTP handler, the exact serving verifier's refresh lifecycle, minimized refresh status, and readiness, so callers cannot supervise or rotate a verifier that is not serving the handler. Every route except liveness fails closed and consumes credential headers unless the refresh lifecycle owns a still-valid generation. Static HTTP and DPoP profile errors fail before the keyset source is contacted.

The provider registry, endpoint-scoped local credential state, authenticated import, published keyset generation, reloadable verifier, and signed configuration now share one exact provider identity and registry digest. Upstream provider credential acquisition and revocation, durable credential-operation audit, group-to-membership administration, HTTPS ingress deployment, provider-side DPoP issuance, production release certification beyond the signed loopback OIDC slice, workload identity, and production conformance remain unresolved. Resource-server nonce enforcement and its capacity evidence are implemented, but nonce selection and thresholds remain explicit deployment choices inside the signed loopback configuration. The opt-in OIDC profile and default static development identity both require explicit loopback listeners and must not bind publicly.


## Signed loopback release certification

`dataground-release-certification` can bind one clean build and released Go runtime to the exact owner-only OIDC security configuration, accepted admission-and-nonce capacity record, Cedar API policy, reviewer attribution, bounded validity window, and deployment-owned Ed25519 trust profile. It prepares the exact domain-separated message for an external signer, verifies the detached signature without loading private key material, and installs a new immutable envelope through durable same-directory linking. Exact replay re-syncs the directory; a different existing envelope conflicts.

The statement cross-checks the capacity record's accepted status, source revision, Go runtime, deployment profile, and explicit nonce selection, then requires the OIDC configuration to name that exact capacity file and digest, Cedar policy path, provider identity, and provider-registry digest. The signed profile remains limited to loopback OIDC and is not provider issuance, ingress evidence, infrastructure certification, or non-loopback activation. See [signed release certification](release-certification.md) for the closed contracts and operator procedure.

OIDC startup requires the installed envelope through `DATAGROUND_RELEASE_CERTIFICATION_FILE` and its public trust profile through `DATAGROUND_RELEASE_CERTIFICATION_TRUST_FILE`. These bootstrap paths deliberately remain outside the signed OIDC configuration to avoid a circular envelope digest. Verification instead requires the signed statement to name and digest the exact configured `DATAGROUND_API_SECURITY_CONFIG_FILE`, then cross-binds its capacity evidence and Cedar policy. The API opens no database connection when certification is missing, invalid, expired, for another clean build, or for another configuration path or content. Every non-liveness request remains unavailable once the incorporated certification expires; renewal or trust rotation requires a new verified envelope and process restart.

## Loopback executable activation

Set `DATAGROUND_API_SECURITY_CONFIG_FILE` to the absolute canonical path of one non-empty regular JSON file no larger than 64 KiB. The configuration file and referenced Cedar policy must not be symlinks or writable by group or other users; the capacity evidence must be owner-only. Startup verifies that every file remains the same path, size, mode, and identity across its bounded read. OIDC mode rejects a simultaneously configured development bearer credential and requires `DATAGROUND_DATABASE_URL` at the current schema.

```json
{
  "contract": "dataground.api-security/oidc-dpop/v5",
  "issuer": "https://identity.example.invalid/realms/dataground",
  "externalOrigin": "https://api.example.invalid",
  "keysetPublicationFile": "/etc/dataground/oidc-keysets.json",
  "algorithms": ["EdDSA"],
  "provider": {
    "id": "primary",
    "registrySha256": "REPLACE_WITH_LOWERCASE_SHA256"
  },
  "jwt": {
    "clockSkew": "30s",
    "maximumLifetime": "1h"
  },
  "dpop": {
    "clockSkew": "30s",
    "maximumProofAge": "1m",
    "nonce": {
      "lifetime": "1m",
      "maximumActivePerKey": 4
    }
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
    "credentialBurst": 10,
    "deploymentProfile": "team",
    "capacityEvidenceFile": "/var/lib/dataground/evidence/admission-capacity.json",
    "capacityEvidenceSha256": "REPLACE_WITH_LOWERCASE_SHA256"
  },
  "authorization": {
    "policySetId": "deployment-api-v1",
    "policyFile": "/etc/dataground/api.cedar"
  }
}
```

Start the opt-in profile with the migrated durable database and explicit configuration path:

```shell
DATAGROUND_DATABASE_URL='<postgresql-url>' \\
DATAGROUND_API_SECURITY_CONFIG_FILE='/etc/dataground/api-security.json' \\
DATAGROUND_RELEASE_CERTIFICATION_FILE='/var/lib/dataground/certifications/release_2026_08_02.json' \\
DATAGROUND_RELEASE_CERTIFICATION_TRUST_FILE='/etc/dataground/release-trust.json' \\
go run ./cmd/dataground-api
```

The values above describe the contract shape, not recommended production capacity. Deployments must select and validate the complete JWT, DPoP, nonce, refresh, admission, and Cedar profile, run the capacity command from the same clean source revision, and replace the evidence digest placeholder with the exact artifact hash. Omitting `dpop.nonce` disables resource-server nonce enforcement; explicit `null`, partial policy, lifetimes outside ten seconds through five minutes, sub-microsecond durations, or overlap counts outside one through sixteen fail startup. The keyset publication uses the separate atomic envelope and owner-operated publisher described above. Startup reads and validates the closed configuration, clean build provenance, filesystem boundaries, Cedar policy, capacity evidence, signed release certification, external origin, refresh policy, nonce policy, and admission policy before contacting PostgreSQL. It then completes the cryptographic profile assembly against the current durable repository and loads the initial keyset. The refresh lifecycle and HTTP server share cancellation: an unexpected lifecycle exit shuts the server down, while stopped or expired refresh ownership, expired release certification, or a changed PostgreSQL capacity profile makes every non-liveness route unavailable and consumes access-token and DPoP headers. Public binding remains prohibited.

## DPoP request binding

The internal `DPoPTokenVerifier` implements the protected-resource proof checks from [RFC 9449](https://www.rfc-editor.org/rfc/rfc9449.html). The DPoP handler requires the `Authorization: DPoP` scheme and rejects DPoP-bound tokens presented as Bearer credentials. It wraps an already cryptographically verified access token, requires that token to carry a valid `cnf.jkt`, and accepts only ES256 or EdDSA proofs with a public embedded JWK. Every proof is bound to one uppercase HTTP method, one trusted canonical HTTPS external URI without query or fragment, its access token through `ath`, a bounded creation window, and a unique proof identifier. Missing, malformed, stale, future, wrong-method, wrong-URI, wrong-token, wrong-key, or replayed proofs fail as invalid credentials.

The optional API request binder supplies that trusted HTTP composition. It accepts one pinned canonical HTTPS origin at assembly, combines it only with the routed request's canonical escaped path, and excludes the query as required by RFC 9449. Caller-controlled host, `Forwarded`, and `X-Forwarded-*` values cannot change the proof URI. Absolute-form targets, dot segments, duplicate separators, non-canonical escaping, duplicate or oversized proof headers, and ambiguous origin forms fail before token verification. The binder removes the DPoP header from the request before authentication continues.

Before a verified token can reach identity resolution, PostgreSQL reserves SHA-256 digests of the JWK thumbprint and proof identifier in one exact isolation domain. Reservation precedes nonce evaluation, so replaying a valid nonce-less proof cannot rotate challenge state. The raw proof, access token, token digest, public key, method, URI, issuer, and subject are not persisted. Active proof reservations are immutable and cannot be deleted; bounded domain-scoped cleanup can reclaim expired rows.

When `dpop.nonce` is present, a valid proof must include one recent resource-server nonce. A missing, malformed, expired, or wrong-key nonce returns `401`, one unpredictable `DPoP-Nonce`, `Cache-Control: no-store`, and a `WWW-Authenticate: DPoP` challenge with `use_dpop_nonce`. PostgreSQL stores only nonce digests under the exact isolation domain and DPoP key digest, accepts the same recent nonce with distinct proof identifiers, keeps only the configured overlap per key, and reclaims expired rows in bounded batches. Database or randomness failure returns authentication unavailable without emitting a challenge. The accepted capacity record covers the exact configured issuance, rotation, and validation profile; broader deployment load and failure certification remain required.

This is not production API activation. A deployment still needs an authorization server that issues DPoP-bound access tokens, reviewed TLS ingress that routes the configured origin without rewriting its canonical path, a reviewed decision to enable and size nonce enforcement, upstream provider credential acquisition and remote revocation, complete production release certification beyond the signed loopback slice, and reviewed non-loopback deployment activation. The executable's OIDC profile therefore remains loopback-only.
