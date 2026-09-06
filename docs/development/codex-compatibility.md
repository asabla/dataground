# Codex native sandbox compatibility candidate

The pinned Codex `0.117.0` binary cannot run restricted commands inside the pinned OpenShell supervisor: its default bubblewrap path needs a denied user namespace, and its explicit legacy Landlock path installs its network filter through the denied `seccomp(SECCOMP_SET_MODE_FILTER)` syscall. This candidate tests a small native-source compatibility change while retaining both the Codex restrictions and the OpenShell supervisor. It does not replace the development profile or produce accepted runtime-conformance evidence.

The patch applies the same compiled network filter through `prctl(PR_SET_SECCOMP, SECCOMP_MODE_FILTER, ...)` only after the ordinary installation returns `EPERM`. Both operations affect the calling thread; failure of the second operation still prevents command execution. Linux stacks the additional filter with its existing filters, and an existing denial takes precedence over an additional allow. See the [kernel's filter installation and precedence documentation](https://www.kernel.org/doc/html/latest/userspace-api/seccomp_filter.html). The existing filesystem restrictions still use Codex's explicit `use_legacy_landlock` feature. The patch does not enable a bypass mode, grant network access, change writable roots, or alter OpenShell policy.

The candidate Dockerfile pins upstream commit `4c70bff480af37b1bf1a9b352b8341060fe55755`, the Rust toolchain image, the existing sandbox base image and a reviewed patch digest. Preparation requires a clean checkout and aligns the release tag's 87 local package-version lock entries with `0.117.0`; it retains all 995 external dependency entries and builds with `--locked`. The image preserves the stock binary and installs experimental binaries under `/opt/dataground-compatibility`. Source metadata, license notices, the installed build-package inventory and binary hashes are retained under `/usr/local/share/dataground-codex-compatibility`. System build packages are acquired from Debian's signed repositories and their versions are recorded; this is a repeatable source build, not a claim of bit-for-bit reproducible output. Use the resulting image digest for a test, rather than a mutable tag.

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

The test creates and removes its own checked loopback gateway and labelled sandbox. It attaches no provider, prevents automatic provider discovery, uses a private empty CLI home, and owns a bounded subprocess lifetime. It compares the stock and experimental native helper and app-server `command/exec` paths. Read-only must deny both workspace and outside writes; workspace-write must allow only the selected workspace. Both modes must deny INET and raw sockets and user-namespace creation. A separate inherited filter denies the alternative installation interface; when both interfaces are unavailable, the candidate must fail before executing its command. Cleanup independently checks that the labelled sandbox has disappeared before removing the gateway.

The credential-free `Codex compatibility candidate` workflow performs the same build and comparison on relevant pull requests or manual dispatch. Local ARM64 comparison has passed. CI supplies independent Linux AMD64 verification; do not infer that it passed from the local result. Inference, command and file approval mediation, artifacts, cancellation, credential non-exposure for this new image, full live conformance, release-image publication and any change to the accepted runtime profile remain separate requirements. The candidate explicitly records `certificationEligible: false`; existing release checkers and the default runtime remain unchanged.
