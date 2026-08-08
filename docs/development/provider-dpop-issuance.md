# Provider DPoP issuance certification

DataGround is an OAuth protected resource, not an authorization server. It does not mint access tokens, register OAuth clients, obtain authorization codes, or hold provider client credentials. A deployment must select and operate an external provider that issues RFC 9449 DPoP-bound access tokens for the exact DataGround audience.

`dataground-provider-dpop-issuance` verifies and immutably installs short-lived, externally signed evidence for that deployment-owned issuance path. The evidence binds one provider identifier and registry digest plus one non-secret OAuth client-profile identifier and digest to the exact issuer, token endpoint, API audience, grant types, DPoP algorithms, authorization-server nonce behavior, and a canonical non-secret conformance report. It cannot turn an unsupported provider or differently configured client registration into a conforming one.

## Conformance report

The deployment-owned runner must create an owner-only canonical JSON report. The report contains no access token, authorization code, refresh token, DPoP private key, client secret, proof JWT, subject, or provider response. Its closed v1 checks require successful token-endpoint proof handling, `token_type=DPoP`, an access-token `cnf.jkt` matching the client proof key, and successful use at the DataGround protected-resource verifier. Missing token-endpoint proofs, proof-key substitution, proof replay, wrong method, wrong URI, and stale token-endpoint proofs must be rejected. The protected-resource checks additionally reject proof-key substitution, replay, wrong method, wrong URI, and a wrong `ath` access-token hash.

`authorizationServerNonce` is `challenge-retry` when the provider challenges with `use_dpop_nonce` and the runner proves one bounded retry using the returned nonce. It is `not-required` only when that exact registered provider/client profile issues DPoP-bound tokens without an authorization-server nonce challenge. This field does not configure DataGround's independent resource-server nonce policy.

The report uses this canonical shape and must end in one newline:

```json
{"contract":"dataground.provider-dpop-issuance-conformance/v1","runId":"provider_run_one","providerId":"primary","providerRegistrySha256":"REPLACE_WITH_LOWERCASE_SHA256","oauthClientProfileId":"primary_web","oauthClientProfileSha256":"REPLACE_WITH_LOWERCASE_SHA256","issuer":"https://identity.example.invalid/realms/dataground","tokenEndpoint":"https://identity.example.invalid/realms/dataground/token","audience":"dataground-api","grantTypes":["authorization_code","client_credentials"],"dpopAlgorithms":["ES256","EdDSA"],"authorizationServerNonce":"challenge-retry","observedAt":"REPLACE_WITH_RFC3339_UTC_TIME","checks":{"tokenEndpointProofAccepted":true,"tokenTypeDpop":true,"confirmationJktMatched":true,"missingTokenEndpointProofRejected":true,"mismatchedTokenEndpointKeyRejected":true,"tokenEndpointProofReplayRejected":true,"wrongTokenEndpointMethodRejected":true,"wrongTokenEndpointUriRejected":true,"staleTokenEndpointProofRejected":true,"resourceProofAccepted":true,"mismatchedResourceKeyRejected":true,"resourceProofReplayRejected":true,"wrongResourceMethodRejected":true,"wrongResourceUriRejected":true,"wrongAccessTokenHashRejected":true}}
```

Grant types and algorithms are sorted, duplicate-free subsets of the closed supported sets. The supported grant evidence names `authorization_code`, `client_credentials`, and `refresh_token`; a deployment includes only the flows it actually exercised. The client-profile digest covers the reviewed non-secret registration settings used by the run, never a client secret. The observation may precede certification issuance by no more than one hour. A changed provider registry, client registration, endpoint, algorithm, grant, nonce mode, or report requires a new report and certification.

## External signature and immutable installation

Create a separate deployment-owned Ed25519 trust profile for issuance reviewers. Do not reuse a provider signing key, release-certification key, token signing key, or DPoP client key.

```json
{"contract":"dataground.provider-dpop-issuance-trust/ed25519/v1","keys":[{"keyId":"provider_reviewer_one","publicKey":"REPLACE_WITH_BASE64URL_ED25519_PUBLIC_KEY"}]}
```

The owner-only canonical statement binds the exact report path and digest. Its validity may not exceed 24 hours, so provider behavior cannot be treated as indefinite release evidence.

```json
{"contract":"dataground.provider-dpop-issuance-certification/v1","certificationId":"provider_certification_one","providerId":"primary","providerRegistrySha256":"REPLACE_WITH_LOWERCASE_SHA256","oauthClientProfileId":"primary_web","oauthClientProfileSha256":"REPLACE_WITH_LOWERCASE_SHA256","issuer":"https://identity.example.invalid/realms/dataground","tokenEndpoint":"https://identity.example.invalid/realms/dataground/token","audience":"dataground-api","grantTypes":["authorization_code","client_credentials"],"dpopAlgorithms":["ES256","EdDSA"],"authorizationServerNonce":"challenge-retry","conformanceReportFile":"/var/lib/dataground/evidence/provider-dpop-issuance-report.json","conformanceReportSha256":"REPLACE_WITH_LOWERCASE_SHA256","trustProfileSha256":"REPLACE_WITH_LOWERCASE_SHA256","issuedAt":"REPLACE_WITH_RFC3339_UTC_TIME","expiresAt":"REPLACE_WITH_BOUNDED_RFC3339_UTC_EXPIRY","reviewerId":"reviewer_one","reason":"certify the exact provider DPoP issuance profile"}
```

Create the report, statement, and trust files with mode `0600` at canonical absolute paths. Prepare the exact domain-separated signing message:

```shell
go run ./cmd/dataground-provider-dpop-issuance \
  -statement-file /run/dataground/provider-dpop-issuance-statement.json \
  -trust-file /etc/dataground/provider-dpop-issuance-trust.json \
  -signing-message-file /run/dataground/provider-dpop-issuance-message.bin
```

An external reviewer signs that exact binary message and returns this canonical detached-signature document:

```json
{"contract":"dataground.provider-dpop-issuance-signature/ed25519/v1","keyId":"provider_reviewer_one","signature":"REPLACE_WITH_BASE64URL_ED25519_SIGNATURE"}
```

Install a new immutable envelope:

```shell
go run ./cmd/dataground-provider-dpop-issuance \
  -statement-file /run/dataground/provider-dpop-issuance-statement.json \
  -signature-file /run/dataground/provider-dpop-issuance-signature.json \
  -trust-file /etc/dataground/provider-dpop-issuance-trust.json \
  -output-file /var/lib/dataground/evidence/provider-dpop-issuance.json
```

The command verifies the exact report again after installation. Exact output replay is read-only; a different envelope at the same path conflicts. Verification later requires the signed statement, trust profile, and exact report to remain available and unchanged, and fails after expiry.

This boundary authenticates review evidence only. The external conformance runner, OAuth client registration, authorization and consent flow, provider credentials, provider-side revocation and incident monitoring, credential custody during the run, and authorization-server availability remain deployment responsibilities.

The loopback API incorporates the envelope only through a v4 signed release statement that names and digests both this installed envelope and its exact trust profile. Release verification revalidates the external signature and canonical report from those digest-checked bytes, requires the evidence provider, registry, issuer, and `dataground-api` audience to match the exact OIDC configuration, and makes readiness expire at the earlier of the provider evidence and release certificate. Replacing either provider artifact requires a new release statement, release signature, and process restart. Reviewed TLS ingress, external conformance execution and monitoring, non-loopback activation, and complete production certification remain blocked.
