# Live governed source worker test

`TestGovernedWorkspaceLiveInvocation` exercises the explicit source worker profile on Linux ARM64 with real PostgreSQL, S3, Rosetta, OpenShell, and Codex. It creates a service revision and an immutable source containing a random value. It installs the source through the object store and catalog, compiles and installs enforcement through the development command, installs the scoped version 6 policy and version 2 source plan, activates the provider grant, publishes the revision, and activates its source grant. The real worker then prepares the source and starts a native turn. The model must copy the source file to a declared artifact. The test compares the exact stored artifact bytes, rejects a cross-domain artifact read, and requires durable source preparation, completion, use-decision, and runtime-attempt records.

This is an internal database-authorized test. Invocation acceptance uses the repository boundary directly, so it does not prove the public API authentication journey. It uses ephemeral test trust and does not establish operational reviewer authority, production backend certification, release acceptance, change journals, checkpoints, or export of all workspace changes. The ordinary test suite skips it unless `DATAGROUND_TEST_GOVERNED_SOURCE_LIVE=true` is set. Missing or invalid prerequisites after that selection fail the test.

Prepare the prerequisites in this order:

1. Acquire and independently pin both published candidate proofs, a fresh model-backed v6 diagnostic, and the Sigstore trusted-root snapshot. Use the [test fixture command](local-runtime-acceptance.md#live-acceptance-verifier-test) to generate a new random scope and temporary acceptance. The test requires its fixed test reviewer and reason and rejects a scope that already contains a service.
2. Start disposable PostgreSQL and the pinned S3 candidate on unused loopback ports, with a pre-existing bucket. The test migrates its selected database. Do not select a database or bucket used for retained work.
3. Build and run the exact Rosetta source required by [development enforcement](development-enforcement.md). Build `cmd/dataground-development-enforcement` for the Linux test host. The test calls that command; it does not substitute checked policy bytes for compilation.
4. Prepare the [retained gateway](development-gateway.md) for the fixture's exact scope and accepted supervisor, and install the [fixed provider](development-provider.md) from a fresh consumed credential bundle. Keep the original gateway observation pins. Use separate private policy and artifact-export workspaces on the Linux host.
5. Set the complete [source worker environment](governed-worker.md#use-an-accepted-source-plan), including the fixture's exact envelope, trust, image, model, source revision, digests, Linux Node.js and GitHub CLI binaries, and current gateway pins. The test calculates and sets its source-plan digest and grant generation after installation. Set `DATAGROUND_TEST_DATABASE_URL`, `DATAGROUND_TEST_ENFORCEMENT_BINARY`, `DATAGROUND_TEST_REPOSITORY_ROOT`, and `DATAGROUND_ROSETTA_CONFORMANCE_ENDPOINT` to the disposable database, built enforcement command, repository root, and running compiler.

Run from the repository root on Linux ARM64:

```sh
DATAGROUND_TEST_GOVERNED_SOURCE_LIVE=true \
go test ./cmd/dataground-worker \
  -run '^TestGovernedWorkspaceLiveInvocation$' -count=1 -timeout=12m -v
```

A macOS host can instead cross-compile the same package with `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c`, then run the resulting test binary on its trusted local Linux host with `-test.run '^TestGovernedWorkspaceLiveInvocation$' -test.timeout 12m -test.v`. The selected Node.js, GitHub CLI, OpenShell CLI, compiler, and enforcement binary must all execute on that same Linux host. Model credentials remain with OpenShell; the worker process does not receive them.

The test requests termination of its created sandbox through the scoped provider record and closes worker resources, including on failure. A cleanup error fails the test. The caller owns the retained gateway, compiler, databases, object store, credential state, and temporary acceptance. Observe sandbox cleanup, stop the exact gateway, remove its stopped container and private state, stop the compiler, and remove only the disposable backends and fixture directory. Retain safe test results and failed attempts. A failed setup can leave catalog or storage state, so do not retry with an occupied scope as though it were new.

The first composed run with the generic Codex adapter reached completed source preparation but failed with `RUNTIME_TURN_FAILED`. Selecting the fixed OpenShell provider corrected that integration gap. A subsequent run on 2026-10-16 passed with the published source-capable image and the v6 diagnostic recorded in [Codex compatibility](codex-compatibility.md). It produced one successful invocation, one source preparation and completion, four allowed source decisions, two allowed provider decisions, and one successful runtime attempt. The exact source bytes were read back from the declared durable artifact and the sandbox was terminated. This is one development conformance result under ephemeral test trust, not an operational acceptance or reliability claim.
