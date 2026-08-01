# OIDC authentication boundary

DataGround has an internal provider-neutral boundary for turning a deployment-verified OIDC access token into a platform principal. It is not composed into the API command and does not make the reference server a production endpoint.

A deployment-owned verifier must validate the token signature, algorithm, issuer, time claims, revocation state, and other provider-specific requirements before returning only the verified issuer, subject, and audiences. DataGround independently requires the configured HTTPS issuer, an exact configured audience, one bounded subject, and a bounded duplicate-free audience set. Cancellation and deadlines remain distinguishable; invalid credentials stay distinct from provider unavailability, and unexpected provider details are not returned.

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

Concrete discovery and signature verification, key rotation and provider revocation behavior, group-to-membership administration, authentication-attempt audit, HTTPS ingress, replay-resistant or sender-constrained credentials, API startup configuration, workload identity, and production conformance remain unimplemented. Until those boundaries exist, the executable API continues to require its loopback-only static development identity and must not bind publicly.
