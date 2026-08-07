# Audit export revocation notice acquisition

The internal `dataground-audit-export-revocation-import` command acquires one recipient-proof or workload-identity revocation notice and its verification profile from an exact operator-selected HTTPS source in an immutable DataGround-published registry. It verifies the signed notice in memory, requires the matching active isolation-scoped revocation-authority and source generations in PostgreSQL, and commits the notice and a minimized source receipt together. The command does not publish downloaded bytes or retain bearer credentials, response bodies, URLs, or external incident detail.

The source registry contract is `dataground.audit-export-revocation-source-registry/v1`. Profiles are sorted by purpose and identifier, bind separate fixed notice and trust URLs on one HTTPS origin, and require independently scoped credential files for the two endpoints. The command verifies the complete canonical registry against the operator-supplied `sha256:` digest before reading either credential. It disables redirects, response compression, environment-selected proxies, runtime endpoint discovery, and non-HTTPS URLs; bounds headers and bodies; accepts only JSON success responses; and enforces TLS 1.2 or newer.

Each credential file uses `dataground.audit-export-revocation-source-credential/v1` and binds one isolation domain, source identifier, exact registry digest, and either the `notice` or `trust` endpoint. Credentials are canonical owner-only files in an owner-only directory, become valid at `activatedAt`, and expire after no more than 24 hours. One importer instance copies each token for one acquisition attempt and clears that copy afterward; the deployment-owned file and upstream token lifecycle remain unchanged. Credential issuance and remote revocation remain deployment-owned.

The internal `dataground-audit-export-revocation-source` command publishes the reviewed registry and governs which exact source profile an importer may use. Its database credential and write access to the owner-only publication directory are the operator authorization boundaries. Each isolation-domain and notice-purpose pair has one sequential append-only generation. An activation reads a separate owner-only input, validates the complete canonical registry without reading credentials, immutably installs it through a synchronized same-directory replacement, verifies the installed bytes, derives its digest, and binds the selected source identifier and registry digest. Exact publication replay is read-only, conflicting reuse of a publication path fails closed, and an uncertain directory sync is recovered by retrying the same operation. A database failure after publication can leave only an unselected valid registry; exact command retry safely completes activation. Rotation must change that source binding; revocation must match the latest activation. Exact historical operation replay is read-only, while a stale, withdrawn, cross-domain, cross-purpose, or caller-substituted registry fails before network access. The database rechecks the same generation while committing the acquisition receipt, serialized with source rotation and revocation-notice intake, so a successful preflight cannot authorize a later stale write.

Activate the reviewed source before importing a notice:

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-revocation-source \
  -operation activate \
  -isolation-domain iso_00000000000000000001 \
  -purpose recipient-proof \
  -source archive-revocations.primary \
  -generation 1 \
  -source-registry-input-file /run/dataground/reviewed/revocation-sources.json \
  -source-registry-file /run/dataground/audit/revocation-sources-1.json \
  -actor operator@example.invalid \
  -reason 'activate the reviewed recipient proof revocation source' \
  -correlation-id cor_00000000000000000001
```

Rotation uses the next generation, another reviewed input, and a new immutable publication path. Publication paths must not be reused for different content. Withdrawal uses `-operation revoke`, the next generation, and `-source-registry-sha256` with the exact active digest instead of either registry-file flag.

After activating the relevant [revocation authority](audit-export-revocation-authorities.md), run the importer with the same exact domain and purpose:

```sh
DATAGROUND_DATABASE_URL='<postgresql-url>' \
go run ./cmd/dataground-audit-export-revocation-import \
  -isolation-domain iso_00000000000000000001 \
  -purpose recipient-proof \
  -source archive-revocations.primary \
  -source-registry-file /run/dataground/audit/revocation-sources-1.json \
  -source-registry-sha256 sha256:REPLACE_WITH_CANONICAL_REGISTRY_DIGEST \
  -actor operator@example.invalid \
  -reason 'acquire the reviewed recipient proof revocation notice' \
  -correlation-id cor_00000000000000000001
```

Use `-purpose workload-identity` for workload issuer notices. The selected registry profile purpose, both endpoint credentials, signed notice domain, trust-profile digest, authority identifier, and signing key must all agree with the command and active PostgreSQL generation. Trust or notice substitution, cross-domain credentials, shared endpoint credentials, redirects, oversized responses, stale credentials, changed replay, and a notice from an inactive authority fail closed.

The append-only acquisition receipt retains only its contract, purpose, signed-notice digest, isolation domain, source identifier, source-registry digest, source generation, trust-profile digest, correlation, and database acquisition time. Before contacting the source, the command checks whether the exact purpose, domain, source, registry, actor, reason digest, and correlation already committed; an exact match returns success without rereading credentials, requiring the source to remain active, or depending on the remote endpoint. Reusing the correlation or signed notice with different provenance or attribution conflicts, and schema rollback refuses to discard acquisition or source-governance evidence.

This boundary proves that DataGround published the exact reviewed canonical registry, selected one of its profiles, and fetched the exact verified notice and matching profile from the currently activated authenticated endpoints before durable intake. The operator still must choose and review suitable external endpoints and a generation-specific publication path. This does not prove external monitoring completeness, source availability or suitability, upstream credential issuance or revocation, authority selection, incident response, evidence retention outside DataGround, or that every relevant notice was published. Recipient identity-proof acquisition, workload grant and certificate issuance, production transport certification, recipient access, retention, legal hold, and deletion remain separate boundaries.
