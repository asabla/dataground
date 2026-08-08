# Signed release certification

DataGround defines a narrow signed certification profile for the loopback OIDC deployment slice. It binds one clean source revision and released Go runtime to the exact OIDC security configuration, accepted admission and DPoP nonce capacity evidence, Cedar API policy, short-lived externally signed provider DPoP issuance evidence and its trust profile, reviewer attribution, validity window, and deployment-owned Ed25519 release trust profile. The profile makes this exact evidence set independently verifiable; it does not execute provider conformance, certify TLS ingress, permit non-loopback activation, or certify images, infrastructure, backup, recovery, or the complete release-candidate gate.

`dataground-release-certification` never generates, loads, or stores a private signing key. A deployment must keep signing in its reviewed HSM, KMS, or offline process. The repository command validates inputs, prepares the exact domain-separated signing message, verifies the detached signature against the pinned public trust profile, and installs a new immutable envelope.

## Trust and statement contracts

Every input is an absolute canonical regular-file path with mode `0600`. JSON inputs are closed, duplicate-free, one-line canonical JSON with one final newline. Trust keys are sorted by `keyId`; one profile may contain at most eight Ed25519 public keys. Key rotation requires a new trust profile, digest, statement, and signature.

The trust profile has this shape. `publicKey` is the raw 32-byte Ed25519 public key encoded as unpadded base64url.

```json
{"contract":"dataground.release-certification-trust/ed25519/v1","keys":[{"keyId":"release_key_01","publicKey":"REPLACE_WITH_UNPADDED_BASE64URL_PUBLIC_KEY"}]}
```

The statement has this exact member and artifact order:

```json
{"contract":"dataground.release-certification/oidc-loopback/v4","releaseId":"release_2026_08_02","sourceRevision":"0123456789abcdef0123456789abcdef01234567","goVersion":"go1.26.5","deploymentProfile":"team","trustProfileSha256":"REPLACE_WITH_LOWERCASE_SHA256","issuedAt":"2026-08-02T12:00:00Z","expiresAt":"2026-08-03T12:00:00Z","reviewerId":"reviewer_01","reason":"reviewed loopback OIDC release evidence","artifacts":[{"kind":"admission-capacity-evidence","file":"/var/lib/dataground/evidence/admission-capacity.json","sha256":"REPLACE_WITH_LOWERCASE_SHA256"},{"kind":"api-authorization-policy","file":"/etc/dataground/api-policy.cedar","sha256":"REPLACE_WITH_LOWERCASE_SHA256"},{"kind":"oidc-security-configuration","file":"/etc/dataground/api-security.json","sha256":"REPLACE_WITH_LOWERCASE_SHA256"},{"kind":"provider-dpop-issuance-certification","file":"/var/lib/dataground/evidence/provider-dpop-issuance.json","sha256":"REPLACE_WITH_LOWERCASE_SHA256"},{"kind":"provider-dpop-issuance-trust-profile","file":"/etc/dataground/provider-dpop-issuance-trust.json","sha256":"REPLACE_WITH_LOWERCASE_SHA256"}]}
```

Version 4 deliberately rejects v3 statements, envelopes, and signing-domain signatures because the earlier artifact set did not contain provider issuance evidence. Before deploying a binary that requires v4, install a new v4 envelope at a new immutable path and update the bootstrap reference for the restarted process. Do not replace or reinterpret retained v3 evidence.

The release trust-profile digest covers its exact canonical bytes, including the final newline. The source revision must match the clean executable build, `goVersion` must match that build and the capacity evidence, issuance cannot be more than five minutes in the future, and validity cannot exceed 31 days. The capacity record must use the v3 contract, be accepted, and match the statement's source revision, Go runtime, and deployment profile. The OIDC configuration must use its v5 contract and refer to the exact signed capacity-evidence path and digest, Cedar-policy path, deployment profile, explicit provider identity and registry digest, and the exact enabled or disabled DPoP nonce policy represented by the evidence. The provider issuance envelope and its separate public trust profile must be exact signed artifacts. Their external signature, report, report digest, provider identity, registry digest, issuer, and `dataground-api` audience are revalidated from the same digest-checked bytes and must match the OIDC configuration.

## Preparation, signing, and installation

Create the release trust profile, statement, and five referenced artifacts with their final bytes and permissions. Produce and install the provider issuance envelope first by following [provider DPoP issuance certification](provider-dpop-issuance.md). Prepare the exact message for the external signer:

```sh
go run ./cmd/dataground-release-certification \
  -statement-file /run/dataground/release-statement.json \
  -trust-file /etc/dataground/release-trust.json \
  -signing-message-file /run/dataground/release-signing-message
```

The command validates every binding before it writes the owner-only message. The message is the ASCII domain `DataGround release certification oidc-loopback v4` followed by a newline and the exact canonical statement bytes. Sign that complete file as raw Ed25519 input in the deployment's external signing system. Place the resulting 64-byte signature in this closed canonical file as unpadded base64url:

```json
{"contract":"dataground.release-certification-signature/ed25519/v1","keyId":"release_key_01","signature":"REPLACE_WITH_UNPADDED_BASE64URL_SIGNATURE"}
```

Install the verified envelope at a new path in a pre-existing directory that is not a symlink or writable by group or other users:

```sh
go run ./cmd/dataground-release-certification \
  -statement-file /run/dataground/release-statement.json \
  -signature-file /run/dataground/release-signature.json \
  -trust-file /etc/dataground/release-trust.json \
  -output-file /var/lib/dataground/certifications/release_2026_08_02.json
```

Installation writes and syncs a same-directory mode-`0600` temporary file, creates the destination without replacement, syncs the directory, and verifies the installed envelope and all referenced artifacts again. Exact replay is read-only and re-syncs the directory; a different existing file conflicts. If installation reports an uncertain filesystem error, preserve the target and adjacent `.dataground-release-certification-*` file for inspection, then retry the exact request. Never reuse an output path for a different statement.

Verify an installed envelope and its live artifact bindings with the same clean build:

```sh
go run ./cmd/dataground-release-certification \
  -verify-file /var/lib/dataground/certifications/release_2026_08_02.json \
  -trust-file /etc/dataground/release-trust.json
```

Removing the signing-message, statement, or signature inputs follows the deployment's approved sensitive-file procedure. The installed envelope and public trust profile are evidence records and should be retained according to the release-evidence policy. OIDC API startup requires their absolute paths through `DATAGROUND_RELEASE_CERTIFICATION_FILE` and `DATAGROUND_RELEASE_CERTIFICATION_TRUST_FILE`; those bootstrap references stay outside the signed configuration to avoid a circular digest, while the statement must name and digest the exact configured OIDC file. Verification happens before PostgreSQL access. Every non-liveness route continuously requires the earlier of the release expiry and provider-issuance expiry; once either is observed expired, clock rollback cannot restore readiness. Renewal, provider evidence replacement, or either trust rotation requires a new release envelope and process restart. A valid envelope proves only the fixed loopback OIDC profile and exact short-lived provider evidence named by its contract; external conformance execution, authorization-server availability, reviewed TLS ingress, the remaining release-candidate evidence, and a separately reviewed non-loopback activation boundary remain required.
