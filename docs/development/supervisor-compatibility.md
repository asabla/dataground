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

The ARM64 compatibility workflow also runs the provider-bound native comparison using the exact previously published Codex candidate. `DATAGROUND_TEST_SUPERVISOR_COMPATIBILITY_IMAGE=sha256:<local-image-id>` selects this additional test boundary; it verifies the supervisor's exact local identity and candidate labels before freezing its run-owned gateway configuration. The test uses Rosetta's unchanged strict artifact and requires native read-only/workspace-write behavior, device/socket/namespace denials, artifact export and owned teardown. It produces no diagnostic or acceptance record. The ordinary launcher and default topology cannot select this private test option. A preceding synthetic credential scan still exercises the original supervisor; this comparison does not claim complete credential non-exposure evidence for the replacement supervisor.

The local ARM64 build passed both kernel tests, all four source-preparation tests and the complete provider-bound comparison with the published Codex image on 2026-09-06. The candidate executable reports upstream's unmodified workspace version `0.0.0`; its source metadata separately identifies the pinned `0.0.86` release commit and exact patch digest. It does not impersonate the released supervisor binary.
