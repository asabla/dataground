# Strict Landlock file compatibility candidate

The pinned OpenShell `0.0.86` supervisor passes directory-only Landlock rights when constructing rules for individual files. A local run of Rosetta's exact strict output failed before runtime initialization: the supervisor rejected `ReadDir` on a file under `HardRequirement` and restarted. A synthetic-only reproduction confirmed the same failure. The earlier fixture policy used the upstream default compatibility behavior, so its successful diagnostic cannot establish strict-policy compatibility.

This candidate retains the pinned upstream source and its complete dependency lock. Its patch identifies the type of the already-opened rule file descriptor, keeps every requested directory right for directories, and intersects file requests with Landlock's ABI-specific file rights. Metadata comes from a duplicate of that same descriptor, so replacing the original path cannot change the selected object type. It leaves strict kernel/ABI requirements, handled rights, allowed paths, network enforcement, and credential mediation intact. Missing paths and failed metadata acquisition still fail closed in the strict privileged preparation path. See the Landlock library's [file-rule compatibility contract](https://landlock.io/rust-landlock/landlock/fn.path_beneath_rules.html).

The checked patch includes kernel tests for file-path replacement and for actual restrictions in an isolated thread: allowed file reads and workspace creation must succeed; writes to a read-only file, access to an outside file, and opening `/dev/zero` must fail while the exact `/dev/null` grant succeeds. Source preparation rejects an altered patch, a different source commit, a dirty checkout, and repeated application. The build retains source metadata, license, package inventory and binary hash.

Build the explicit candidate on a disposable Linux Docker host with Landlock support:

```sh
docker build --tag dataground-supervisor-compatibility:candidate deploy/openshell/supervisor-compatibility
docker image inspect --format '{{.Id}}' dataground-supervisor-compatibility:candidate
```

The digest-pinned Rust image matches upstream's toolchain and builds a musl supervisor. Signed Alpine repositories provide build packages, whose versions are recorded; this does not claim bit-for-bit reproducibility. The final image replaces only the supervisor executable in the existing pinned supervisor image and identifies itself as an uncertified candidate. It is not selected by the default topology, does not activate a worker profile, and supplies no runtime or production certification.

The ARM64 compatibility workflow also runs the provider-bound native comparison using the exact previously published Codex candidate. `DATAGROUND_TEST_SUPERVISOR_COMPATIBILITY_IMAGE=sha256:<local-image-id>` selects this additional test boundary; it verifies the supervisor's exact local identity, upstream source, patch digest and non-certification labels before freezing its run-owned gateway configuration. The test uses Rosetta's unchanged strict artifact and requires native read-only/workspace-write behavior, device/socket/namespace denials, artifact export and owned teardown. It produces no diagnostic or acceptance record. The default topology does not select this candidate.

`canarylauncher.CheckSupervisorCandidate` separately runs all seven synthetic credential-exposure surfaces against that exact local supervisor/runtime image pair and the same strict Rosetta policy. The workflow requires this scan before the native comparison. It inspects process state, environment, readable filesystem, provider arguments, gateway logs, sandbox logs and the app-server error stream, then requires owned cleanup. Both images must pass bounded identity/label inspection before any topology is created; labels identify local operator-selected candidates and are not publication attestations. This path returns only success or failure, rejects entry through the certifying launcher, and cannot supply stock-profile evidence. The complete strict scan passed locally on ARM64 on 2026-09-06 using synthetic values only; no real provider credentials were acquired.

The local ARM64 build passed both kernel tests, all four source-preparation tests and the complete provider-bound comparison with the published Codex image on 2026-09-06. The candidate executable reports upstream's unmodified workspace version `0.0.0`; its source metadata separately identifies the pinned `0.0.86` release commit and exact patch digest. It does not impersonate the released supervisor binary.

The supervisor workflow can publish an ARM64 candidate only through an explicit manual run on this repository's reviewed `main` branch with `publish=true`. Its read-only build job requires the kernel, synthetic credential and native restriction/export checks before saving the exact local image into a digest-bound archive. A separate job downloads that exact workflow artifact, checks the source revision, supervisor-specific transfer contract, ARM64 architecture, Linux platform, source and patch labels, and non-certification metadata, then publishes to `ghcr.io/asabla/dataground-supervisor-candidate` with a source/run/attempt-specific tag. The supervisor retains the pinned base image's empty Docker user setting; sandbox workload identity still comes from the strict policy's `sandbox` user and group. Codex's separate transfer profile continues to require its `sandbox` Docker user.

The publication job alone receives ephemeral registry and signing permissions. It attests the exact published digest through the pinned GitHub/Sigstore action and always logs out of GHCR. The shared helper at `deploy/openshell/candidate-publication.py` accepts only the two fixed Codex and supervisor profiles; their transfer contracts, metadata and registry destinations cannot be interchanged. For example, `prepare --candidate supervisor --architecture arm64 --source-commit <reviewed-sha> --image sha256:<local-id> --directory <new-private-directory>` selects the supervisor boundary. Publication is disabled by default and cannot run from a PR. A push or signature alone is insufficient: consumers must independently verify the signed invocation and successful completion of the exact workflow attempt. This change incorporates no published supervisor digest, signed acceptance, or production certification.

Verify a supervisor publication with GitHub CLI 2.98.0 and Docker available locally:

```sh
pnpm openshell:supervisor:candidate:check \
  "sha256:$SUPERVISOR_DIGEST" "$REVIEWED_SOURCE_COMMIT" \
  "$PUBLICATION_RUN_ID" "$PUBLICATION_RUN_ATTEMPT" arm64
```

The verifier requires the supervisor repository and workflow, exact source and signer revisions, hosted-runner identity, main ref, and signed run/attempt. It independently checks successful completion of that exact attempt and both `strict-landlock` and `publish-candidate` jobs before pulling the digest. The pulled Linux ARM64 image must retain the expected supervisor user setting, source and patch labels, repository digest, and non-certification metadata. Codex signatures, workflow jobs, and image metadata cannot substitute for the supervisor profile.

Offline verification accepts the exact raw manifest, attestation bundle, and independently acquired trust roots. The acquisition, owner-only file requirements, and independent trust-root pin follow the [Codex candidate procedure](codex-compatibility.md). Add the raw configuration blob addressed by the signed manifest to verify its architecture and execution metadata:

```sh
pnpm openshell:supervisor:candidate:attestation:check \
  "sha256:$SUPERVISOR_DIGEST" "$REVIEWED_SOURCE_COMMIT" \
  "$PUBLICATION_RUN_ID" "$PUBLICATION_RUN_ATTEMPT" \
  /absolute/private/manifest.json /absolute/private/bundle.jsonl \
  /absolute/private/trusted-root.jsonl "$REVIEWED_TRUST_ROOT_SHA256" \
  arm64 /absolute/private/image-config.json
```

The architecture and configuration arguments can be omitted together for signature-only verification. Offline verification freezes bounded private snapshots and invokes `gh` with isolated state and no inherited credentials. It cannot establish workflow completion or filesystem-layer contents. Both commands identify `openshell-supervisor-candidate/v1` and remain non-certifying; neither accepts a runtime diagnostic nor activates a governed worker.

The explicit local runtime launcher can select the image with `--supervisor-candidate-image sha256:<verified-local-supervisor-id>` together with `--local-diagnostic --policy-profile rosetta-development/v1 --candidate-image sha256:<verified-local-runtime-id> --model <available-model-id>` and the existing private workspace, credential-bundle and frozen-source arguments. It first runs the complete strict synthetic scan, starts the exact candidate topology, and checks its frozen gateway bytes before consuming the local credential bundle. The returned v5 diagnostic records both image identities, the realized gateway-configuration digest, the Rosetta compiler/input/policy identity and the supervisor source/patch metadata. All twelve runtime cases and cleanup gates still apply.

Verify such a record with `pnpm openshell:runtime-diagnostic:check <record.json> --source-commit <exact-commit> --candidate-image sha256:<runtime-id> --supervisor-candidate-image sha256:<supervisor-id>`. Both images must be supplied independently; the verifier recomputes the realized gateway digest from the checked template and exact selected supervisor. A missing binding, changed template, substituted image, patch drift or downgrade to an older record version fails. The v5 record remains non-certifying, and the existing signed v3 acceptance/worker profile cannot consume it. No successful v5 runtime record is incorporated by this launcher change.
