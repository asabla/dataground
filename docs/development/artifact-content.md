# Governed artifact content reads

The artifact-content endpoint is `GET /v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/artifacts/{artifactId}/content`. It requires the separate Cedar action `readInvocationArtifactContent` on the exact artifact in the authenticated isolation domain. Permission to read artifact metadata does not grant this action. The canonical API Cedar schema and its policy digest include the new action; deployments must use the resulting exact policy provenance.

The durable handler checks authorization at entry, before the object read, and after verification before disclosure. Each evaluation uses the same authenticated principal, path-derived artifact identity, isolation domain, and request correlation. In the command's durable development profile, authentication and authorization decisions commit to the existing PostgreSQL audit boundaries. An audit failure prevents content release. Migration 63 adds this action to the closed audit constraint and refuses downgrade after any content-read decision has been recorded.

The reader requires an available catalog record with the exact domain, invocation, and artifact ID. It reads only the key derived from that immutable record. It verifies the complete byte count and SHA-256 digest, then reads the catalog again to reject a removed or changed binding. Missing or foreign invocation bindings return `RESOURCE_NOT_FOUND`. Corrupt, oversized, missing, or unavailable objects return `ARTIFACT_CONTENT_UNAVAILABLE` without partial artifact bytes or upstream errors. Cancellation prevents disclosure. These checks do not add deletion or retention policy.

Successful responses use `application/octet-stream`, an attachment filename derived from the artifact ID, `Cache-Control: no-store`, and `X-Content-Type-Options: nosniff`. The quoted ETag is the catalog digest. Names and stored media types cannot cause inline execution. The endpoint returns complete objects and does not implement conditional or range responses; range requests and query parameters are rejected. Clients must treat transport truncation as a failed transfer and compare the received bytes with the metadata digest and size before use.

The configured maximum is at most 16 MiB. Four transfers can hold content buffers per handler, including blocked response writes; excess requests fail with the same retryable unavailable error. Catalog, storage, and authorization work has a 15-second request context. The command's object transport also has a 10-second timeout. The HTTP response writer must support a 15-second write deadline; a writer without that capability cannot release content. These bounds are development limits, not capacity evidence or production certification.

## Explicit loopback configuration

The API command leaves content transfer disabled by default. Process-local mode and the certified OIDC command profile cannot select this configuration. To enable transfer in durable bearer-authenticated development mode, write an owner-only configuration file with the worker's exact endpoint and bucket. For example:

```json
{
  "contract": "dataground.api-artifact-content/v1",
  "endpoint": "http://127.0.0.1:8333",
  "bucket": "dataground-development",
  "maximumBytes": 16777216
}
```

Set `DATAGROUND_API_ARTIFACT_CONTENT_CONFIG_FILE` to that file when starting `pnpm dev:api`, together with the existing `DATAGROUND_DATABASE_URL` and development identity configuration. The example bucket must be replaced if the governed worker uses another bucket. The file reader rejects duplicate or unknown fields, non-loopback endpoints, URL credentials, paths, query strings, invalid buckets, and unsupported limits. HTTP proxy use and redirects are disabled. No object-store credentials are acquired or passed to clients.

The API accepts this opt-in with ordinary durable mode, exact governed dispatch, or reviewed publication dispatch. It does not establish an OIDC-certified content profile. Production storage authentication, retention and access policy, larger transfers, and release acceptance remain unresolved.

## Workbench content reads

Artifact inspection first loads metadata. “Read content” makes a separate request with the current client identity. The Workbench bounds the successful body to the declared size, at most 16 MiB, and bounds error bodies to 8 KiB. It rejects redirects, unexpected status or media type, size or ETag changes, truncated or extra bytes, and SHA-256 mismatch. Upstream error messages are not displayed. A read has a 30-second abort signal, and cancellation also cancels its body reader.

Only verified bytes can create a local download link. “Download verified file” uses an artifact-ID filename with a `.bin` extension and `application/octet-stream`; the original bytes are unchanged. Plain UTF-8 text and JSON up to 1 MiB also appear as escaped plain text. Controls that could alter text direction or terminal-style presentation are shown as Unicode escapes. Other media, invalid UTF-8, and larger files remain available through the verified download. No HTML, Markdown, image, or document renderer executes artifact content.

“Hide content,” metadata refresh, identity or scope changes, and navigation cancel pending reads and revoke local download URLs. Late responses cannot restore content. Deleted, unknown-state, oversized, or unconfirmed artifacts have no active content-read control. Failed reads leave metadata inspection available and permit a new explicit attempt.

The shared content pattern keeps keyboard focus on the read, cancel, or hide action. Its text region is keyboard-scrollable, and waiting, completion, and error messages have semantic status or alert roles, following [WCAG status-message guidance](https://www.w3.org/WAI/WCAG22/Understanding/status-messages.html). Browser tests verify the complete metadata-to-verified-content flow, byte-exact download URLs, cancellation, corruption, identity and scope replacement, refresh, and URL revocation. Storybook covers keyboard operation and accessibility. These checks do not establish a production deployment or user-study result.

## Verification

Focused tests cover metadata-only permission, denials at each authorization boundary, corrupt and changed objects, unavailable storage, scope mismatches, cancellation, bounded response concurrency, and strict configuration. The catalog integration fixture exercises the authenticated HTTP route with real PostgreSQL audit and an HTTP object endpoint. The existing live S3 candidate suite verifies the object transport separately. Neither a test endpoint nor the disposable backend establishes production storage certification.
