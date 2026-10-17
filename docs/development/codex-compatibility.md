# Codex native sandbox compatibility candidate

The pinned Codex `0.117.0` binary cannot run restricted commands inside the pinned OpenShell supervisor: its default bubblewrap path needs a denied user namespace, and its explicit legacy Landlock path installs its network filter through the denied `seccomp(SECCOMP_SET_MODE_FILTER)` syscall. This candidate tests a small native-source compatibility change while retaining both the Codex restrictions and the OpenShell supervisor. It does not replace the development profile or produce accepted runtime-conformance evidence.

The patch applies the same compiled network filter through `prctl(PR_SET_SECCOMP, SECCOMP_MODE_FILTER, ...)` only after the ordinary installation returns `EPERM`. Both operations affect the calling thread; failure of the second operation still prevents command execution. Linux stacks the additional filter with its existing filters, and an existing denial takes precedence over an additional allow. See the [kernel's filter installation and precedence documentation](https://www.kernel.org/doc/html/latest/userspace-api/seccomp_filter.html). The existing filesystem restrictions still use Codex's explicit `use_legacy_landlock` feature. The patch does not enable a bypass mode, grant network access, change writable roots, or alter OpenShell policy.

The candidate Dockerfile pins upstream commit `4c70bff480af37b1bf1a9b352b8341060fe55755`, the Rust toolchain image, the existing sandbox base image and a reviewed patch digest. Preparation requires a clean checkout and aligns the release tag's 87 local package-version lock entries with `0.117.0`; it retains all 995 external dependency entries and builds with `--locked`. Source metadata, license notices, the installed build-package inventory and binary hashes are retained under `/usr/local/share/dataground-codex-compatibility`. System build packages are acquired from Debian's signed repositories and their versions are recorded; this is a repeatable source build, not a claim of bit-for-bit reproducible output. Use the resulting image digest for a test, rather than a mutable tag.

Inside this experimental image, build-time activation verifies the source metadata and both binary hashes, preserves the stock native executable at `/opt/dataground-compatibility/stock-codex`, and installs the candidate at the existing npm package's native executable path. The `/usr/bin/codex` launcher and provider-authorized path remain unchanged. The image's read-only `/etc/codex/config.toml` enables `features.use_legacy_landlock = true`, so DataGround's fixed `codex app-server` command uses the candidate without a transport command override. Existing configuration or unexpected paths fail the build before replacement. `runtime-launch.json` records the exact stock and candidate binary hashes, native path and configuration hash. This activation affects only the explicitly built experimental image; the default development image and accepted profile remain unchanged.

On a disposable Linux Docker host, build the candidate and run its explicit integration test with the checksum-verified OpenShell `0.0.86` CLI:

```shell
docker build --tag dataground-codex-compatibility:candidate deploy/openshell/codex-compatibility
candidate_image=$(docker image inspect --format '{{.Id}}' dataground-codex-compatibility:candidate)
DATAGROUND_TEST_CODEX_COMPATIBILITY_IMAGE="$candidate_image" \
DATAGROUND_TEST_RUNTIME_TOPOLOGY_ROOT="$PWD" \
DATAGROUND_TEST_OPENSHELL_BINARY='<absolute-pinned-cli-path>' \
go test ./internal/execution/runtimeevidence \
  -run '^TestCodexNativeSandboxCompatibility$' -count=1 -timeout=6m -v
```

The test creates and removes its own checked loopback gateway and labelled sandbox. It attaches no provider, prevents automatic provider discovery, uses a private empty CLI home, and owns a bounded subprocess lifetime. It compares the stock and experimental native helper and app-server `command/exec` paths, then repeats the app-server cases through the normal `codex app-server` launch without command-line feature overrides. Read-only must deny both workspace and outside writes; workspace-write must allow only the selected workspace. Both modes must deny INET and raw sockets and user-namespace creation. A separate inherited filter denies the alternative installation interface; when both interfaces are unavailable, the candidate must fail before executing its command. Cleanup independently checks that the labelled sandbox has disappeared before removing the gateway.

The credential-free `Codex compatibility candidate` workflow performs the same build and comparison on relevant pull requests or manual dispatch. The original explicit-path comparison passed on local ARM64 and CI Linux AMD64 in PR #256, and the normal-launch comparison passed on both architectures in PR #257.

The workflow also scans the exact built image with the existing synthetic provider credential checker. This separate opt-in test covers sandbox process state, environment, readable filesystem, provider arguments, gateway logs, sandbox logs and the actual app-server error stream. It requires the expected candidate source label, non-certifying label and `sandbox` image user before provisioning. A dedicated internal `CreateLocalDiagnostic` operation permits an exact local Docker image ID only in the pinned loopback topology; ordinary execution creation continues to require a registry digest. Both paths use the same placement, policy, provider selection and recovery checks. Candidate scan results retain the actual image identity internally and cannot serialize as accepted credential evidence.

Run this scan after the native comparison on the same disposable Linux host, with the same environment variables and image digest:

```shell
DATAGROUND_TEST_CODEX_COMPATIBILITY_IMAGE="$candidate_image" \
DATAGROUND_TEST_RUNTIME_TOPOLOGY_ROOT="$PWD" \
DATAGROUND_TEST_OPENSHELL_BINARY='<absolute-pinned-cli-path>' \
go test ./internal/security/canarylauncher \
  -run '^TestCandidateCredentialNonExposure$' -count=1 -timeout=12m -v
```

The candidate scan has passed locally on ARM64. Its workflow result must independently establish AMD64 coverage. It uses only generated synthetic credentials, and all seven scans plus resource cleanup must succeed. The local diagnostic below establishes one combined ARM64 inference, approval, artifact, cancellation and teardown run. Release-image publication and any change to the accepted runtime profile remain separate requirements. The candidate explicitly records `certificationEligible: false`; existing release checkers and the default runtime remain unchanged.

The [local runtime diagnostic](openshell-local.md#exact-runtime-certification-manifests) accepts this image through `--candidate-image sha256:<exact-local-image-id>` only with `--local-diagnostic` and an explicit model. It reruns the synthetic scan before opening local credentials, then exercises the existing twelve live cases against the selected image. Its separate v3 diagnostic report records that actual image and the exact runtime-policy digest and omits the stock credential-evidence digest. This command acquires no credentials from GitHub and cannot generate accepted CI evidence.

Provider-bound execution also requires the checked `deploy/openshell/codex-compatibility/runtime-policy.yaml`. In OpenShell `0.0.86`, attaching the Codex provider enables the [proxy-mode Landlock baseline](https://github.com/NVIDIA/OpenShell/blob/d556748771c41cbbd4e4dd7cd9030c798afe2b7d/crates/openshell-sandbox/src/lib.rs#L1105), which omits `/dev/null`. Codex then fails opening that device for child standard input before it can emit command events. The candidate policy grants read/write access to that device only; it does not grant `/dev` or add network endpoints. Provider-authorized inference retains its existing route. The launcher and execution creator independently verify the policy digest, and candidate diagnostic v3 binds it as `profile.runtimePolicySHA256`. This policy is selected for candidate diagnostics and the explicitly accepted experimental worker profile; it does not alter the stock conformance or credential-evidence profile.

`TestCodexProviderBoundSandboxCompatibility` uses synthetic credentials and DataGround’s existing SSH transport with the normal `codex app-server` launch. It reproduces the missing-device denial under the original policy, then requires native read-only and workspace-write commands to execute under the candidate policy. It checks null-device access, unrelated-device denial, workspace and outside-write behavior, INET/raw socket denial, user-namespace denial, native artifact production and exact export under `/sandbox`, and cleanup. The candidate workflow runs this test independently of inference.


## Recorded local diagnostic

The exact [ARM64 candidate diagnostic](../../deploy/openshell/diagnostics/codex-candidate-arm64-20260906.json) passed all twelve live cases on 2026-09-06 using source commit `fed7191b2543c53842cb8310149ff3c359e8c6f5`, model `gpt-6-astra`, and local candidate image `sha256:703abdf5d88c6298423ba25cb11340990169b4f535b1b75ecc9fb4b730165573`. It exercised initialization, successful and unavailable-model turns, command-event normalization, interruption, cancellation, denied command and file approvals, artifact production/export, and sandbox teardown on the owned loopback topology. The candidate synthetic credential scan ran before local credentials were opened. The local bundle and live run resources were removed; the synthetic precheck retained only its empty mode-0600 policy-workspace lock.

The archived JSON retains its exact producer bytes, with SHA-256 `4ff6e86de99891700d312c19f42aee4c5c69000110a54070a2f805a44209f665`. Its narrow formatter override preserves that identity, and a regression test pins the digest. The current verifier rejects this archived report because it predates the explicit gateway command and current Compose digest. Its bytes remain historical evidence of the original run. A new live diagnostic is required before local acceptance under the corrected topology.

This verifier checks recorded observations; it does not independently rerun the sandbox or authenticate an operator attestation. The record has local origin and `certificationEligible: false`, and the CI evidence schema rejects it. It contains no credentials, prompts, native transcript, or exported content. It establishes this local ARM64 run only and does not itself establish image publication. An explicit local-evidence acceptance contract, accepted credential/runtime evidence, scoped activation, and production certification remain outstanding. Default execution and release checkers are unchanged.


## Experimental image publication

The candidate workflow can test native `amd64` or `arm64` on explicit dispatch; pull requests retain the AMD64 regression. Dispatch with `architecture: arm64` and `publish: true` from `main` to publish that architecture after the helper, native, synthetic credential, and provider-bound checks pass. Publication requests from another branch or repository fail. No model credentials are available to either job.

The build job retains read-only repository permissions and packages the exact tested local image with its source revision, architecture and archive digest. A separate job consumes that immutable workflow artifact, verifies the archive and the loaded image’s identity, `sandbox` user and non-certifying source labels, then publishes to `ghcr.io/asabla/dataground-codex-candidate` using a run-and-attempt-specific candidate tag. Consumers must pin the returned registry digest. The job uses only its ephemeral GitHub registry token in the disposable runner’s default Docker credential file, which the pinned attestation action reads directly, and always logs out of GHCR afterward. The published digest receives GitHub/Sigstore build provenance from the pinned official attestation action.

A pushed image whose attestation or workflow later fails is not a completed publication. The workflow must complete successfully, and consumers must verify its repository/workflow provenance before using the image. Publication remains experimental and architecture-specific: it neither promotes the accepted development profile nor supplies inference conformance or production certification. The publication helper’s denial tests run before every candidate build.

Verify a publication with GitHub CLI 2.98.0 and Docker available locally. Supply the exact reviewed workflow source revision, image digest, run ID, attempt, and tested architecture; do not derive the expected source from untrusted image labels:

```sh
pnpm codex:candidate:check \
  "sha256:$CANDIDATE_DIGEST" "$REVIEWED_SOURCE_COMMIT" \
  "$PUBLICATION_RUN_ID" "$PUBLICATION_RUN_ATTEMPT" arm64
```

The command verifies GitHub/Sigstore provenance through `gh attestation verify`, pins the repository identity, workflow, issuer, main ref, source and signer revisions, hosted runner and exact invocation, then checks that the same workflow attempt and both jobs completed successfully. A signature issued before a later workflow failure is rejected. Only afterward does Docker pull the exact registry digest and verify its architecture, local image ID, `sandbox` user and candidate labels. The command uses existing local GitHub access when required and acquires no model credentials. Upstream command output is withheld on failure.

Successful output records the registry digest, local image ID, source, architecture and invocation with `certificationEligible: false`. This output is a verification summary, not a signed acceptance record; rerun verification before use. It does not accept local runtime evidence or activate governed execution.

For offline signature verification, retain the exact raw manifest from `docker buildx imagetools inspect --raw`, its bundle from `gh attestation download`, and trust roots acquired through `gh attestation trusted-root`. Pin the trust-root SHA-256 independently at that authenticated acquisition boundary; a digest supplied alongside untrusted roots does not make those roots trustworthy. Store the three files as owner-only regular files, then run:

```sh
pnpm codex:candidate:attestation:check \
  "sha256:$CANDIDATE_DIGEST" "$REVIEWED_SOURCE_COMMIT" \
  "$PUBLICATION_RUN_ID" "$PUBLICATION_RUN_ATTEMPT" \
  /absolute/private/manifest.json /absolute/private/bundle.jsonl \
  /absolute/private/trusted-root.jsonl "$REVIEWED_TRUST_ROOT_SHA256"
```

This command requires GitHub CLI 2.98.0 and checks the same exact certificate and subject bindings as the online publication verifier. It reads bounded, owner-held non-symlink files, checks the manifest and trust-root digests, freezes private copies, and runs the verifier with an isolated configuration/state directory and no inherited credentials. The child receives explicit offline inputs and HTTP(S) proxy settings pointing to loopback port 9; it acquires no remote evidence. Temporary inputs and CLI state are removed after success or failure. Without a configuration blob, offline verification establishes the signed image digest and invocation only. It does not check workflow completion, accept a runtime diagnostic, or enable governed execution. Its output explicitly retains `publicationCompletionChecked: false` and `certificationEligible: false`.

To also verify image architecture and isolation metadata, append the expected architecture and a private file containing the exact raw configuration blob addressed by the manifest:

```sh
pnpm codex:candidate:attestation:check \
  "sha256:$CANDIDATE_DIGEST" "$REVIEWED_SOURCE_COMMIT" \
  "$PUBLICATION_RUN_ID" "$PUBLICATION_RUN_ATTEMPT" \
  /absolute/private/manifest.json /absolute/private/bundle.jsonl \
  /absolute/private/trusted-root.jsonl "$REVIEWED_TRUST_ROOT_SHA256" \
  arm64 /absolute/private/image-config.json
```

The configuration uses the same bounded private-file acquisition rules. The verifier checks its exact byte length and SHA-256 against the signed Docker v2 or OCI image manifest, then requires the selected Linux architecture, `sandbox` user and candidate source/non-certification labels. Docker inspection output is a reserialization and cannot replace the original blob. Successful output includes the configuration digest and verified execution metadata. This verifies metadata committed by the signed manifest; it does not acquire or verify the filesystem layers. A local Docker image ID can identify either the manifest or its configuration, depending on the image store, so callers must retain both exact identities when relating a local diagnostic to the published image.

## Source-capable candidate images

The candidate workflow now adds the [workspace helper layer](workspace-source-materialization.md#internal-sandbox-transfer) after the unchanged Codex source build. It compiles the helper with the repository-pinned Go version, disables cgo, and builds for Linux on the native runner architecture. A private temporary build context contains only the compiled helpers and the selected Dockerfile. The final layer requires an absent workspace, preserves the `sandbox` user, and installs the helper as root-owned mode `0555` code. It records the materialization contract and exact DataGround source revision as image labels. Temporary build inputs are removed before publication's clean-checkout check.

CI runs source transfer and recovery against the exact final image, without rebuilding or mounting a replacement helper. It checks helper ownership and mode, the private sandbox parent, an initially absent workspace, binary content, executable mode, lost-acknowledgement read-back, and refusal to overwrite a modified tree. The existing native restriction and synthetic credential tests also use that final image. Source-helper changes trigger the candidate workflow. The source-transfer test uses a controlled SSH peer; it does not establish live gateway source authorization. The provider-bound native test separately sets `DATAGROUND_TEST_WORKSPACE_SOURCE_REQUIRED=true` to exercise the actual OpenShell gateway and provider profile. Before starting the harness, it rejects cross-domain transfer, installs a binary file, a contained symlink, and an executable, repeats exact read-back, and rejects a different source without changing the installed tree. The native runtime then verifies the exact bytes, private modes and link, and runs the prepared executable under its read-only policy. The existing workspace-write, outside-write, socket, namespace, and artifact-export checks remain required. This test uses synthetic source and provider credentials; it does not supply a source grant or signed acceptance statement.

Earlier source-capable workflow publications use the internal transfer record `dataground.codex-workspace-candidate-publication/v1`. Preparation, loading, and publication require the helper contract and source-revision label to match the reviewed checkout. The older Codex and supervisor transfer records remain distinct and cannot replace this record. The registry repository and attestation signer remain unchanged.

Use the separate source-capable verifier when an image must contain this helper:

```sh
node scripts/check-workspace-candidate.mjs publication \
  "sha256:$CANDIDATE_DIGEST" "$REVIEWED_SOURCE_COMMIT" \
  "$PUBLICATION_RUN_ID" "$PUBLICATION_RUN_ATTEMPT" arm64
```

Offline verification requires the raw image configuration as well as the existing private manifest, bundle, and independently pinned roots:

```sh
node scripts/check-workspace-candidate.mjs attestation \
  "sha256:$CANDIDATE_DIGEST" "$REVIEWED_SOURCE_COMMIT" \
  "$PUBLICATION_RUN_ID" "$PUBLICATION_RUN_ATTEMPT" \
  /absolute/private/manifest.json /absolute/private/bundle.jsonl \
  /absolute/private/trusted-root.jsonl "$REVIEWED_TRUST_ROOT_SHA256" \
  arm64 /absolute/private/image-config.json
```

Both commands require the helper contract and DataGround source revision in the signed image configuration, in addition to the existing Codex identity and isolation checks. Successful output identifies `openshell-codex-workspace-candidate/v1` and remains non-certifying. Offline verification still does not prove workflow completion. Source-free verification remains available for historical images; its success does not establish the helper capability. The [version 3 local acceptance](local-runtime-acceptance.md#source-capable-runtime-acceptance) and explicit [source worker profile](governed-worker.md#use-an-accepted-source-plan) consume this image profile with separate acceptance, plan, policy, and source-grant pins. Image verification alone does not authorize source use.

## Cleanup and change-export candidate images

The candidate workflow adds `workspace-source/Owned.Dockerfile` after source materialization. This layer installs `dataground-native-owner` and `dataground-workspace-export` as root-owned mode `0555` binaries, retains the `sandbox` user, and records `dataground.native-owner/v1`, `dataground.workspace-changes/v1` and the exact helper source revision. It contains no test binary. The source and export revision labels must both match the reviewed workflow source.

New workflow publications use `dataground.codex-workspace-export-candidate-publication/v1`. This record cannot be replaced by the earlier source-only record. CI tests the installed native owner with the actual Codex initialization exchange and terminal cleanup, then verifies binary change export and lost-acknowledgement recovery from the same final image. These Docker tests use a controlled SSH peer and no provider credentials. The strict supervisor workflow uses this same layer definition and adds only its kernel-test binary for the separate gateway and detached-writer checks.

Require the complete helper set with:

```sh
node scripts/check-workspace-export-candidate.mjs publication \
  "sha256:$CANDIDATE_DIGEST" "$REVIEWED_SOURCE_COMMIT" \
  "$PUBLICATION_RUN_ID" "$PUBLICATION_RUN_ATTEMPT" arm64
```

The `attestation` subcommand takes the same arguments as the source-capable verifier above, including the raw image configuration. Both forms return `candidateProfile: "openshell-codex-workspace-export-candidate/v1"`. Missing helpers, mismatched contract versions or mixed source revisions fail verification. Offline verification binds this metadata through the signed manifest but does not prove workflow completion.

The v3 local runtime acceptance still selects only the source capability. This new publication profile does not activate owned-runtime execution, authorize change export or sandbox deletion, or establish release acceptance. It supplies the image boundary for a later explicit runtime-acceptance contract and worker integration.

## Published source-capable ARM64 candidate

[Publication run 38810366494, attempt 1](https://github.com/asabla/dataground/actions/runs/38810366494/attempts/1) passed its source-transfer, native, credential-exposure, provider-bound execution, and publication checks from reviewed source `36e3a5b6c86dc5437970777328e212f0932a19f7`. It published `ghcr.io/asabla/dataground-codex-candidate@sha256:59a5758504cf6a8e91a4889cde4836a13002d86b6d409e3e1457d7387814393d`. The online source-capable verifier checked the exact signed invocation, completed jobs, pulled ARM64 image, and helper labels. Offline verification also checked the raw manifest and configuration against the signed digest. The configuration digest is `sha256:73fbae520ae9d655c490ddc9df11266cd8e12922acf68ae52fa5cc3121253f21`.

A local v6 diagnostic on 2026-10-15 used that image, the independently verified published supervisor `sha256:57cc182a7c56cf1fbd64af8b09b2196eea727783eb9ec0c2292542e3be0f755c`, clean checkout `939258249cb05f628c16d4c81de26b6afc299063`, the strict Rosetta policy, and model `gpt-6-astra`. The run reached the file-change approval case but failed at `case-file-change-approval-approval-observation`. No diagnostic record was emitted. The launcher removed its sandbox, gateway, provider binding, and runtime workspace; the fresh credential bundle was absent after the run. The synthetic precheck's empty policy-workspace lock remained. This failed attempt does not establish model-backed conformance or signed acceptance.

A second run from clean source `1aefedb55515746c7167795d43fc89cc984d47b3` requested file-change approval and sent the denial, then failed at `case-file-change-approval-interrupt`. It also produced no success record and removed its live resources. A deterministic regression then exposed a mismatch in the probe: it requested a patch under `/sandbox` with `/tmp` as its working directory, while the adapter correctly rejects completed patch paths outside that directory, including denied patches. The probe now uses `/sandbox` with native read-only sandboxing. This still requires file-change approval and keeps the denied patch within the adapter's output boundary. The denial, interruption, and cleanup checks remain required; this correction alone does not establish live conformance.

The corrected probe then produced a [valid v6 diagnostic](../../deploy/openshell/diagnostics/codex-workspace-candidate-arm64-20261015.json) from clean source `24d922dd9c74e0825453689173df2469bdafa120`, using the same two published images and model. Source preparation and native read-back finished before the twelve live cases, which passed from `2026-10-15T22:06:36.413185269Z` through `2026-10-15T22:07:24.039499664Z`. The record retains the original producer bytes with SHA-256 `9c8cfe68c0b772e3c076402db9e4a656b93eabbc670a514b645e0cd209dffe8f`. Its closed-schema verifier passed with independently supplied source and image pins. The fresh credential bundle and live resources were removed; only the synthetic precheck's empty policy-workspace lock remained before operator scratch cleanup. The two earlier failed attempts remain documented above. This is one successful local source-capable diagnostic, not signed operational acceptance, a complete governed-worker run, or release certification.

Local approval failures now distinguish a turn that succeeds, fails, or is cancelled without requesting approval from an unclassified observation failure. These fixed stage names contain no model output, native identifiers, or approval payloads. The classification adds no retry, changes no approval decision, and releases no evidence from a failed probe. It does not explain the earlier failure retrospectively.

## Published ARM64 candidate diagnostic

The [local acceptance command](local-runtime-acceptance.md) can bind a diagnostic and its independently verified published image to an explicit, signed development target. It remains a separate contract from CI certification; the worker requires an explicit experimental profile and independent acceptance pins to consume it.

[Publication run 34025809311, attempt 1](https://github.com/asabla/dataground/actions/runs/34025809311/attempts/1) completed successfully from reviewed source `e7a0839bfcaa6a9d95540224a63c02be45bb89e1`. Its ARM64 native, synthetic credential, and provider-bound/export checks passed before publishing `ghcr.io/asabla/dataground-codex-candidate@sha256:9fff9875097a3608fce25e0d401cacc70ad10113237683fe907e45d94e4b24a1`. GitHub/Sigstore provenance was uploaded to both the repository and registry, and registry logout completed. The local publication verifier then checked the exact signed invocation and successful jobs, pulled the digest, and verified its ARM64 architecture, `sandbox` user and candidate labels. The downloaded manifest and signed bundle also passed offline provenance verification with the authenticated trust roots.

The [published-image local diagnostic](../../deploy/openshell/diagnostics/codex-published-candidate-arm64-20260906.json) passed all twelve cases using that image, the same frozen DataGround source and local model `gpt-6-astra`. The run lasted from `2026-09-06T10:14:04.051999581Z` to `2026-09-06T10:14:45.542614448Z`; its exact producer bytes have SHA-256 `f9fa0ab4183cc3d6d588160da3ec9523c6858c4da0955d97b0e2abb83f59f7aa`. The synthetic scan preceded credential use. The local bundle and live resources were removed; only the synthetic precheck's empty policy-workspace lock remained. No model credentials were supplied to GitHub.

```sh
pnpm codex:candidate:check \
  sha256:9fff9875097a3608fce25e0d401cacc70ad10113237683fe907e45d94e4b24a1 \
  e7a0839bfcaa6a9d95540224a63c02be45bb89e1 34025809311 1 arm64
```

This records a verified publication and one historical local conformance run of that exact image. The archived diagnostic predates the corrected gateway command and does not pass the current topology verifier. The diagnostic remains local and non-certifying. It does not satisfy the existing CI runtime-evidence contract, activate the governed worker, or certify the complete Developer, Team, or Production deployment profile.
