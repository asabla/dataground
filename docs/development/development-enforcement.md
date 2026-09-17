# Install strict development enforcement

`dataground-development-enforcement` compiles the checked strict development input through the pinned Rosetta HTTP client, then installs the exact resulting policy through the immutable S3/PostgreSQL finalizer. It fills the enforcement-object and catalog prerequisites for the strict governed worker. It does not publish a service revision, install its execution plan, grant provider access, accept a runtime, or provide production policy distribution.

The command accepts only the repository's exact `deploy/openshell/codex-compatibility/rosetta-runtime-input.json` bytes, pinned by SHA-256. It requires Rosetta compiler `1.0.0`, catalog `rosetta/v1`, strict mode, and target contract `rosetta/openshell-policy-v1`. The client verifies the compiler's input and artifact digests, capability decisions, target schema, and isolation-scoped binding. Installation additionally requires the exact checked policy digest `sha256:a1d56c0470c3264c4c37183352d783ebb67911d92ef2eb6ec5f7c76c61f69f39`. No alternate source, output, compiler response, or fallback policy is accepted.

The operator must run the reviewed Rosetta candidate from source revision `320158f1e4a4eea378d82c1527f4a7af5fb9855b`. CI builds that exact commit and tests this command against it. The live HTTP protocol verifies the version and target contract; it does not attest the server's source commit. The catalog source-revision field records this required operator-owned deployment pin. A trusted loopback deployment is required, and this command cannot turn the unsigned candidate into a certified Rosetta release.

Use the current PostgreSQL schema, an existing draft or published `codex.app-server/v1` revision whose required capability list is exactly `codex.app-server/v1`, and a pre-existing development S3 bucket. Both HTTP endpoints must be literal loopback addresses with explicit ports, without credentials, paths, query strings, or fragments. The command uses anonymous path-style S3, disables ambient HTTP proxies, and refuses redirects. Administrative database access authorizes the operation; the supplied actor and correlation identify its append-only audit entry. No provider credential is acquired or read.

```sh
DATAGROUND_DATABASE_URL='<administrative-postgresql-url>' \
go run ./cmd/dataground-development-enforcement \
  --repository-root /absolute/path/to/dataground \
  --isolation-domain iso_00000000000000000001 \
  --revision rev_00000000000000000001 \
  --actor development-operator \
  --correlation-id enforcement-installation-1 \
  --rosetta-endpoint http://127.0.0.1:18089 \
  --s3-endpoint http://127.0.0.1:8333 \
  --s3-bucket dataground-conformance
```

The command checks the revision scope and runtime before contacting Rosetta or storage. It writes the policy at a deterministic isolation-domain, revision, bundle, and digest-scoped object key, reads back the exact bytes, then binds the immutable catalog metadata and audit record in one database transaction. The audit contains only artifact and binding digests. Compiled content, Cedar input, and object routing remain internal.

On success, stdout contains one `dataground.dev.enforcement-installation/v1` JSON receipt with `isolationDomainId`, `revisionId`, `bundleId`, `digest`, `bindingDigest`, and `certificationEligible: false`. Use `bundleId` and `digest` in the reviewed execution plan. A matching digest identifies the installed bytes; it does not grant execution authority or establish release acceptance.

Retry the complete original command after an unavailable result or lost stdout. Identical materialization reuses the deterministic object and catalog binding without another audit entry. Conflicting bytes, metadata, or scoped identities fail. A failed database write can leave an unbound object; exact replay can adopt it. The command has no object deletion authority and does not report rollback after an ambiguous effect. Its two-minute deadline includes compilation and storage operations. Errors withhold compiler, storage, and database payloads.

This is an experimental local provisioning path. Signed compiler deployment identity, a certified release, production transport, complete publication admission, and production storage authorization remain required for the normative production boundary.
