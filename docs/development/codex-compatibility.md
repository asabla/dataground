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

The candidate scan has passed locally on ARM64. Its workflow result must independently establish AMD64 coverage. It uses only generated synthetic credentials, and all seven scans plus resource cleanup must succeed. Inference, command and file approval mediation, artifacts, cancellation, full live conformance, release-image publication and any change to the accepted runtime profile remain separate requirements. The candidate explicitly records `certificationEligible: false`; existing release checkers and the default runtime remain unchanged.
