# Audit export revocation notice acquisition

The internal `dataground-audit-export-revocation-import` command acquires one recipient-proof or workload-identity revocation notice and its verification profile from an exact deployment-owned HTTPS source. It verifies the signed notice in memory, requires the matching active isolation-scoped revocation-authority generation in PostgreSQL, and commits the notice and a minimized source receipt together. The command does not publish downloaded bytes or retain bearer credentials, response bodies, URLs, or external incident detail.

The source registry contract is `dataground.audit-export-revocation-source-registry/v1`. Profiles are sorted by purpose and identifier, bind separate fixed notice and trust URLs on one HTTPS origin, and require independently scoped credential files for the two endpoints. The command verifies the complete canonical registry against the operator-supplied `sha256:` digest before reading either credential. It disables redirects, response compression, environment-selected proxies, runtime endpoint discovery, and non-HTTPS URLs; bounds headers and bodies; accepts only JSON success responses; and enforces TLS 1.2 or newer.

Each credential file uses `dataground.audit-export-revocation-source-credential/v1` and binds one isolation domain, source identifier, exact registry digest, and either the `notice` or `trust` endpoint. Credentials are canonical owner-only files in an owner-only directory, become valid at `activatedAt`, and expire after no more than 24 hours. One importer instance copies each token for one acquisition attempt and clears that copy afterward; the deployment-owned file and upstream token lifecycle remain unchanged. Credential issuance and remote revocation remain deployment-owned.

After activating the relevant [revocation authority](audit-export-revocation-authorities.md), run the importer with the same exact domain and purpose:

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-revocation-import \
  -isolation-domain iso_00000000000000000001 \
  -purpose recipient-proof \
  -source archive-revocations.primary \
  -source-registry-file /run/dataground/audit/revocation-sources.json \
  -source-registry-sha256 sha256:REPLACE_WITH_CANONICAL_REGISTRY_DIGEST \
  -actor operator@example.invalid \
  -reason 'acquire the reviewed recipient proof revocation notice' \
  -correlation-id cor_00000000000000000001
```

Use `-purpose workload-identity` for workload issuer notices. The selected registry profile purpose, both endpoint credentials, signed notice domain, trust-profile digest, authority identifier, and signing key must all agree with the command and active PostgreSQL generation. Trust or notice substitution, cross-domain credentials, shared endpoint credentials, redirects, oversized responses, stale credentials, changed replay, and a notice from an inactive authority fail closed.

The append-only acquisition receipt retains only its contract, purpose, signed-notice digest, isolation domain, source identifier, source-registry digest, trust-profile digest, correlation, and database acquisition time. Before contacting the source, the command checks whether the exact purpose, domain, source, registry, actor, reason digest, and correlation already committed; an exact match returns success without rereading credentials or depending on the remote endpoint. Reusing the correlation or signed notice with different provenance or attribution conflicts, and schema rollback refuses to discard acquisition evidence.

This boundary proves that DataGround fetched the exact verified notice and matching profile from the registered authenticated endpoints before durable intake. It does not prove external monitoring completeness, source availability, upstream credential issuance or revocation, authority selection, incident response, evidence retention outside DataGround, or that every relevant notice was published. Recipient identity-proof acquisition, workload grant and certificate issuance, production transport certification, recipient access, retention, legal hold, and deletion remain separate boundaries.
