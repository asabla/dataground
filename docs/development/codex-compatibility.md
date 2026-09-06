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

Provider-bound execution also requires the checked `deploy/openshell/codex-compatibility/runtime-policy.yaml`. In OpenShell `0.0.86`, attaching the Codex provider enables the [proxy-mode Landlock baseline](https://github.com/NVIDIA/OpenShell/blob/d556748771c41cbbd4e4dd7cd9030c798afe2b7d/crates/openshell-sandbox/src/lib.rs#L1105), which omits `/dev/null`. Codex then fails opening that device for child standard input before it can emit command events. The candidate policy grants read/write access to that device only; it does not grant `/dev` or add network endpoints. Provider-authorized inference retains its existing route. The launcher and execution creator independently verify the policy digest, and candidate diagnostic v3 binds it as `profile.runtimePolicySHA256`. This policy is selected only for candidate diagnostics; it does not alter the stock conformance or credential-evidence profile.

`TestCodexProviderBoundSandboxCompatibility` uses synthetic credentials and DataGround’s existing SSH transport with the normal `codex app-server` launch. It reproduces the missing-device denial under the original policy, then requires native read-only and workspace-write commands to execute under the candidate policy. It checks null-device access, unrelated-device denial, workspace and outside-write behavior, INET/raw socket denial, user-namespace denial, native artifact production and exact export under `/sandbox`, and cleanup. The candidate workflow runs this test independently of inference.


## Recorded local diagnostic

The exact [ARM64 candidate diagnostic](../../deploy/openshell/diagnostics/codex-candidate-arm64-20260906.json) passed all twelve live cases on 2026-09-06 using source commit `fed7191b2543c53842cb8310149ff3c359e8c6f5`, model `gpt-6-astra`, and local candidate image `sha256:703abdf5d88c6298423ba25cb11340990169b4f535b1b75ecc9fb4b730165573`. It exercised initialization, successful and unavailable-model turns, command-event normalization, interruption, cancellation, denied command and file approvals, artifact production/export, and sandbox teardown on the owned loopback topology. The candidate synthetic credential scan ran before local credentials were opened. The local bundle and live run resources were removed; the synthetic precheck retained only its empty mode-0600 policy-workspace lock.

The archived JSON retains its exact producer bytes, with SHA-256 `4ff6e86de99891700d312c19f42aee4c5c69000110a54070a2f805a44209f665`. Its narrow formatter override preserves that identity, and a regression test pins the digest. Verify the report's closed shape, current profile, exact image/source, case order, distinct commitments, nanosecond chronology, and run-bound cleanup with:

```shell
pnpm openshell:runtime-diagnostic:check \
  deploy/openshell/diagnostics/codex-candidate-arm64-20260906.json \
  --source-commit fed7191b2543c53842cb8310149ff3c359e8c6f5 \
  --candidate-image sha256:703abdf5d88c6298423ba25cb11340990169b4f535b1b75ecc9fb4b730165573
```

This verifier checks recorded observations; it does not independently rerun the sandbox or authenticate an operator attestation. The record has local origin and `certificationEligible: false`, and the CI evidence schema rejects it. It contains no credentials, prompts, native transcript, or exported content. It establishes this local ARM64 run only: reviewed image publication, an explicit local-evidence acceptance contract, accepted credential/runtime evidence, scoped activation, and production certification remain outstanding. Default execution and release checkers are unchanged.
