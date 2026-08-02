# Verification and release gates

## Pull-request gate

- Source requirement or ADR is linked.
- Public contract changes include schemas, examples and compatibility analysis.
- Unit/contract tests cover success, denial, invalid input, cancellation and retry where relevant.
- Database changes include migration, mixed-version behavior and rollback/forward-repair notes.
- Logs, traces and audit avoid sensitive payloads and include required correlation fields.
- Generated files are reproducible and clean after regeneration.
- UI changes cover keyboard, focus, loading, empty, error, narrow viewport and dark/high-contrast themes.
- Diff is reviewed for scope expansion, duplicated abstractions and bypasses of product boundaries.

## Checkpoint gate

Every milestone in the first vertical slice must demonstrate:

1. A clean checkout can build and run it using documented commands.
2. The deterministic reference runtime passes end-to-end.
3. Idempotency and duplicate delivery are tested.
4. Restarting API/worker processes does not lose accepted commands or event replay.
5. Cross-domain identifiers and data cannot collide.
6. Safe errors contain correlation IDs without upstream secrets.
7. Telemetry and audit correlate the same operation.
8. Operator repair is explicit and repeatable.
9. Documentation and `AGENTS.md` match verified behavior.

## Real-runtime certification gate

- Exact runtime, adapter, image and schema versions are pinned.
- Capability manifest is captured and compared with required/optional revision capabilities.
- Recorded upstream fixtures and live conformance both pass.
- Interrupt, cancel, question, approval, tool, process, file, artifact, usage, failure and resume behavior are classified.
- Raw provider credentials are absent from process environment, arguments, files, logs, crash output and tool-visible state.
- Upstream endpoints are loopback/internal and never become product URLs.
- Upgrade and prior-matrix rollback are proven.

## Execution-provider certification gate

- Provision/create is idempotent across timeout and lost acknowledgement.
- Policy attachment and hash/provenance are observed.
- Gateway selection honors hard constraints and records an explanation.
- Drain and loss stop new placement.
- Service routing remains internal.
- Logs, file/artifact export and cleanup work without gateway credentials reaching callers.
- Orphan detection and teardown are safe to repeat.
- Driver-specific limitations are published, not silently emulated.

## Security gate

Run the full specification security matrix plus:

- threat model reviewed for every new trust boundary;
- authorization at entry and before every consequential external effect;
- SSRF and arbitrary target tests for callbacks, tools and data bindings;
- hostile input through prompts, notebook outputs, schemas, filenames, archives and event payloads;
- dependency, license, secret, source, IaC and image scans;
- signed images, SBOM and provenance verified at admission;
- content deletion and minimal audit tombstone tested across active state, objects, indexes, caches, checkpoints and backups;
- operator-content separation and emergency revocation exercised.

## Resilience gate

Inject failure:

- before and after database commit;
- before and after outbox publication;
- before and after every external operation;
- during gateway acknowledgement, event streaming, artifact finalization and cancellation;
- during worker/API restart, database failover, object-store latency, network partition and gateway loss;
- during runtime upgrade, checkpoint, restore, health validation, traffic switch and cleanup.

The result must be one completed effect, an explicit recoverable state, or a safe terminal failure—never silent loss, duplicate external effects, widened access or indefinite ambiguity.

## Frontend and design-system gate

- WCAG 2.2 AA automated checks pass for stable stories and complete journeys.
- Manual keyboard navigation, focus order and focus visibility pass.
- At least one supported screen-reader/browser pair passes critical journeys; the certified matrix records exact pairs.
- 200% zoom and narrow reflow preserve function.
- Reduced motion, RTL, localization expansion and high-contrast themes pass.
- Reconnect/event replay does not duplicate misleading announcements.
- Unknown states/events degrade explicitly.
- Approval and destructive-action text identifies target, scope and consequence.

## Release-candidate gate

- Signed release certification manifest contains exact source revision, images/digests, schemas, database version, runtime matrix, gateway/driver matrix, object/catalog implementations, identity profile and test evidence.
- Install, upgrade, rollback, backup/restore, gateway drain, stuck-state repair and incident runbooks pass from clean environments.
- Local Docker/Podman and local Kubernetes pass declared parity tests.
- Production reference profile passes load, soak, failure, recovery and upgrade tests.
- Measured capacity envelope and overload behavior are published.
- Known limitations, optional degradation and unsupported combinations are explicit.
- Rosetta-dependent functionality remains disabled unless its contract and conformance gate pass.

The signed loopback OIDC certification profile covers only its exact identity configuration, Cedar policy, accepted admission evidence, source revision, and Go runtime. The API requires that envelope and its trust profile before durable OIDC startup and removes non-liveness readiness at expiry. It does not satisfy the complete release-candidate gate or authorize public activation.

## Evidence record

Each gate stores:

- source/version/digest;
- test identity and environment;
- command or automated job reference;
- structured result and logs/artifacts;
- reviewer and approval where required;
- exception, expiry and owner if waived.

No permanent waiver is allowed for isolation, raw-credential exposure, authorization bypass, unsigned production images, unrecoverable durable state, or unrepresentable policy grants.
