# OIDC authentication boundary

DataGround has an internal provider-neutral boundary for turning a verified OIDC access token into a platform principal, plus a concrete verifier for one immutable JWT access-token profile. Neither boundary is composed into the API command, and neither makes the reference server a production endpoint.

`PinnedOIDCJWTVerifier` verifies compact JWS access tokens against a deployment-owned JWKS using the security-fixed `go-jose` 4.1.4 release. The profile pins the exact HTTPS issuer, the canonical DataGround API audience, an explicit asymmetric algorithm allowlist, clock skew, and maximum token lifetime. Tokens require protected `alg` and `kid` headers, valid `exp` and `iat` claims, one bounded subject, and a bounded duplicate-free audience set containing the API audience. Expired, not-yet-valid, excessive-lifetime, wrong-issuer, wrong-audience, unknown-key, mismatched-algorithm, malformed, and duplicate-member tokens fail as invalid credentials. Cancellation and deadlines remain distinguishable, and library or provider details are not returned.

A pinned JWKS is limited to 256 KiB and 64 public signing keys. Every key requires a unique bounded `kid`, an exact allowed `alg`, and `use` set to `sig`; private, symmetric, weak RSA, mismatched ECDSA, certificate-reference, and unknown members fail at assembly. The verifier accepts reviewed overlap between old and new keys for manual rollover, but it never discovers or fetches an unpinned key. Changing the key set requires explicit configuration replacement and process recomposition.

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

PostgreSQL-backed durable API mode now wraps its configured authenticator in a fail-closed attempt-audit boundary. Each completed attempt records only its path-derived isolation domain, generated request correlation, credential method, and coarse outcome. Successful in-domain attempts additionally record the platform principal identifier and kind; rejected, unavailable, and cross-domain attempts contain no principal data. Tokens, token digests, issuer, subject, audience, key ID, request path, client address, and dependency diagnostics are never part of this record. The current executable still supplies only the loopback development authenticator; the same internal boundary can wrap OIDC when production composition prerequisites exist.

OIDC discovery, automatic key refresh, provider revocation behavior, group-to-membership administration, HTTPS ingress, replay-resistant or sender-constrained credentials, API startup configuration, workload identity, and production conformance remain unimplemented. Until those boundaries exist, the executable API continues to require its loopback-only static development identity and must not bind publicly.


## DPoP request binding

The internal `DPoPTokenVerifier` implements the protected-resource proof checks from [RFC 9449](https://www.rfc-editor.org/rfc/rfc9449.html). It wraps an already cryptographically verified access token, requires that token to carry a valid `cnf.jkt`, and accepts only ES256 or EdDSA proofs with a public embedded JWK. Every proof is bound to one uppercase HTTP method, one trusted canonical HTTPS external URI without query or fragment, its access token through `ath`, a bounded creation window, and a unique proof identifier. Missing, malformed, stale, future, wrong-method, wrong-URI, wrong-token, wrong-key, or replayed proofs fail as invalid credentials.

The request scope must be supplied by trusted HTTP composition after canonical external URI reconstruction; forwarded headers are not accepted by this package. Before a verified token can reach identity resolution, PostgreSQL reserves SHA-256 digests of the JWK thumbprint and proof identifier in one exact isolation domain. The raw proof, access token, token digest, public key, method, URI, issuer, and subject are not persisted. Active reservations are immutable and cannot be deleted; bounded domain-scoped cleanup can reclaim expired rows.

This is not API activation. A deployment still needs an authorization server that issues DPoP-bound access tokens, reviewed TLS ingress and proxy trust, canonical URI reconstruction, DPoP header extraction, optional nonce policy, OIDC provider lifecycle, rate limiting, and API startup composition. The executable therefore continues to accept only its loopback development identity.
