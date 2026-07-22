import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const profile = JSON.parse(
  await readFile(resolve(root, "deploy/storage/enforcement-conformance-profile.json"), "utf8"),
);
const compose = await readFile(resolve(root, "deploy/storage/seaweedfs-conformance.yml"), "utf8");
const workflow = await readFile(resolve(root, ".github/workflows/ci.yml"), "utf8");
const recoverySuite = await readFile(
  resolve(root, "internal/execution/recoveryconformance/suite.go"),
  "utf8",
);
const commitLossSuite = await readFile(
  resolve(root, "internal/execution/recoveryconformance/commit_loss.go"),
  "utf8",
);
const connectionLossSuite = await readFile(
  resolve(root, "internal/execution/recoveryconformance/commit_connection_loss.go"),
  "utf8",
);
const preCommitConnectionLossSuite = await readFile(
  resolve(root, "internal/execution/recoveryconformance/pre_commit_connection_loss.go"),
  "utf8",
);
const failoverSuite = await readFile(
  resolve(root, "internal/execution/recoveryconformance/failover.go"),
  "utf8",
);
const failoverCommitSuite = await readFile(
  resolve(root, "internal/execution/recoveryconformance/failover_commit_loss.go"),
  "utf8",
);
const failoverRejoinSuite = await readFile(
  resolve(root, "internal/execution/recoveryconformance/failover_rejoin.go"),
  "utf8",
);
const primaryInit = await readFile(
  resolve(root, "deploy/storage/postgres-primary-init.sh"),
  "utf8",
);
const standbyEntrypoint = await readFile(
  resolve(root, "deploy/storage/postgres-standby-entrypoint.sh"),
  "utf8",
);
const primaryEntrypoint = await readFile(
  resolve(root, "deploy/storage/postgres-primary-entrypoint.sh"),
  "utf8",
);
const fenceScript = await readFile(resolve(root, "deploy/storage/postgres-fence.sh"), "utf8");
const rejoinScript = await readFile(resolve(root, "deploy/storage/postgres-rejoin.sh"), "utf8");
const commitProxy = await readFile(
  resolve(root, "internal/execution/recoveryconformance/pgcommitproxy/proxy.go"),
  "utf8",
);
const routeProxy = await readFile(
  resolve(root, "internal/execution/recoveryconformance/pgrouteproxy/proxy.go"),
  "utf8",
);
const routeState = await readFile(
  resolve(root, "internal/execution/recoveryconformance/pgrouteproxy/route_state.go"),
  "utf8",
);
const routeCommand = await readFile(
  resolve(root, "cmd/dataground-postgres-route-conformance/main.go"),
  "utf8",
);
const routeSupervisor = await readFile(
  resolve(root, "cmd/dataground-postgres-route-conformance/supervisor.go"),
  "utf8",
);
const routeManager = await readFile(
  resolve(root, "cmd/dataground-postgres-route-conformance/manager.go"),
  "utf8",
);
const routeChildOwnership = await readFile(
  resolve(root, "cmd/dataground-postgres-route-conformance/child_ownership.go"),
  "utf8",
);
const routeChildOwnershipLinux = await readFile(
  resolve(root, "cmd/dataground-postgres-route-conformance/child_ownership_linux.go"),
  "utf8",
);
const routeOwnershipLinux = await readFile(
  resolve(root, "cmd/dataground-postgres-route-conformance/supervisor_ownership_linux.go"),
  "utf8",
);
const poolCommand = await readFile(
  resolve(root, "cmd/dataground-postgres-route-conformance/pool.go"),
  "utf8",
);
const recoveryImplementation = `${recoverySuite}\n${commitLossSuite}\n${connectionLossSuite}\n${preCommitConnectionLossSuite}\n${failoverSuite}\n${failoverCommitSuite}\n${failoverRejoinSuite}`;
const recoveryCommand = await readFile(
  resolve(root, "cmd/dataground-enforcement-recovery-conformance/main.go"),
  "utf8",
);
const packageManifest = JSON.parse(await readFile(resolve(root, "package.json"), "utf8"));

function fail(message) {
  throw new Error(message);
}

if (
  profile.schemaVersion !== "dataground.dev.s3-enforcement-profile/v1" ||
  profile.status !== "development-candidate" ||
  profile.productionCertifiable !== false
) {
  fail("the enforcement-object backend profile must remain a non-production candidate");
}

const backend = profile.backend ?? {};
if (
  backend.name !== "SeaweedFS" ||
  backend.version !== "4.40" ||
  backend.sourceCommit !== "875cd1f67ea25e8965a4f5ba1e6aaf501ba6b6fa" ||
  backend.license !== "Apache-2.0" ||
  backend.image !==
    "chrislusf/seaweedfs@sha256:52194fba4fecd0083c842158b3a902ba6e04a63619b2b0efcd08007bdb6a4602"
) {
  fail("the SeaweedFS candidate identity is not pinned exactly");
}

const expectedPlatforms = ["linux/386", "linux/amd64", "linux/arm/v7", "linux/arm64"];
if (
  !Array.isArray(backend.platforms) ||
  JSON.stringify([...backend.platforms].sort()) !== JSON.stringify(expectedPlatforms)
) {
  fail("the candidate multi-architecture image evidence is incomplete");
}

const requiredCases = [
  "missing-read",
  "create-read",
  "immutable-rewrite",
  "concurrent-create",
  "finalizer-lost-ack",
  "finalizer-catalog-retry",
  "finalizer-conflict",
];
if (
  profile.conformance?.reportSchema !== "dataground.s3-enforcement-conformance/v1" ||
  profile.conformance?.concurrentWriters !== 8 ||
  profile.conformance?.ciRequired !== true ||
  JSON.stringify(profile.conformance?.requiredCases) !== JSON.stringify(requiredCases)
) {
  fail("the enforcement-object conformance contract is incomplete");
}

const recoveryPhases = {
  prepare: ["fresh-scope", "fixture-provisioned", "object-retained-after-database-loss"],
  outage: ["finalization-fails-closed-during-object-outage", "catalog-remains-unbound"],
  recover: [
    "retained-object-present",
    "concurrent-catalog-adoption-after-restarts",
    "read-only-replay",
    "single-audit-commit",
  ],
  "committed-recover": [
    "catalog-commit-survived-process-loss",
    "object-consistent-after-process-loss",
    "read-only-replay-after-ambiguous-commit",
    "single-audit-after-ambiguous-commit",
  ],
  "commit-connection-loss": ["commit-connection-loss-preconditions", "commit-result-ambiguous"],
  "connection-loss-recover": [
    "catalog-commit-observed-after-connection-loss",
    "object-consistent-after-connection-loss",
    "read-only-replay-after-connection-loss",
    "single-audit-after-connection-loss",
  ],
  "pre-commit-connection-loss": [
    "pre-commit-connection-loss-preconditions",
    "pre-commit-result-ambiguous",
  ],
  "rolled-back-recover": [
    "rollback-observed-after-pre-commit-loss",
    "retained-object-adopted-after-rollback",
    "read-only-replay-after-rollback",
    "single-audit-after-rollback-adoption",
  ],
  "failover-recover": [
    "promoted-standby-has-replicated-fixture",
    "retained-object-unbound-after-primary-loss",
    "catalog-adopted-on-promoted-standby",
    "read-only-replay-after-failover",
    "single-audit-after-failover",
  ],
  "failover-commit-loss": [
    "in-flight-failover-preconditions",
    "primary-loss-during-commit-is-ambiguous",
  ],
  "failover-commit-recover": [
    "promoted-standby-ready-after-in-flight-loss",
    "atomic-catalog-outcome-observed-after-failover",
    "catalog-converged-after-in-flight-failover",
    "read-only-replay-after-in-flight-failover",
    "single-audit-after-in-flight-failover",
  ],
  "failover-rejoin-observe": [
    "rewound-primary-rejoined-read-only",
    "rejoined-standby-has-converged-catalog",
    "read-only-replay-on-rejoined-standby",
    "single-audit-on-rejoined-standby",
  ],
};
if (
  profile.recoveryConformance?.reportSchema !== "dataground.enforcement-recovery-conformance/v1" ||
  profile.recoveryConformance?.database !== "PostgreSQL 18.4 disposable test dependency" ||
  profile.recoveryConformance?.freshRunnerProcesses !== true ||
  profile.recoveryConformance?.postgresServerRestart !== true ||
  profile.recoveryConformance?.objectBackendOutage !== true ||
  profile.recoveryConformance?.objectContainerRecreated !== true ||
  profile.recoveryConformance?.concurrentRecoveryWorkers !== 8 ||
  profile.recoveryConformance?.clusteredFailover?.phase !== "failover-recover" ||
  profile.recoveryConformance?.clusteredFailover?.mode !==
    "PostgreSQL physical streaming replication" ||
  profile.recoveryConformance?.clusteredFailover?.nodes !== 2 ||
  profile.recoveryConformance?.clusteredFailover?.replicationBoundary !==
    "standby replay at or beyond captured primary WAL position" ||
  profile.recoveryConformance?.clusteredFailover?.promotionParameterSymmetry !==
    "identical WAL sender, slot, retention and hint settings" ||
  profile.recoveryConformance?.clusteredFailover?.primaryLoss !==
    "primary container stopped before promotion" ||
  profile.recoveryConformance?.clusteredFailover?.promotion !== "explicit pg_ctl promotion" ||
  profile.recoveryConformance?.clusteredFailover?.automaticElection !== false ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.address !==
    "127.0.0.1:55431" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.clientContract !==
    "unchanged database URL before and after promotion" ||
  JSON.stringify(profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.targets) !==
    JSON.stringify(["127.0.0.1:55432", "127.0.0.1:55433"]) ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.control !==
    "mode-0600 Unix socket in an exclusive mode-0700 private state workspace" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.routeChange !==
    "durably persisted control-triggered generation-bound health confirmation after external promotion" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.existingSessions !==
    "closed on route change without transaction replay" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.healthSelection?.probe !==
    "PostgreSQL recovery, transaction read-only state and WAL timeline through a non-owner identity without application table privileges" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.healthSelection?.targets !==
    "both startup-predeclared loopback endpoints probed concurrently" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.healthSelection
    ?.acceptance !==
    "three consecutive observations of one writable primary on the expected PostgreSQL timeline" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.healthSelection
    ?.confirmations !== 3 ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.healthSelection
    ?.confirmationIntervalMillis !== 200 ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.healthSelection
    ?.generationBinding !== "caller-supplied exact PostgreSQL timeline ID" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.healthSelection
    ?.staleGeneration !== "rejected without route or session change" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.healthSelection
    ?.concurrentRouteChange !== "invalidates in-progress confirmation" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.healthSelection
    ?.zeroWritable !== "rejected without route or session change" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.healthSelection
    ?.multipleWritable !== "rejected without route or session change" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.healthSelection
    ?.promotionOwnership !== "external" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.restartRecovery?.state !==
    "canonical mode-0600 cluster-scoped record binding route, PostgreSQL timeline and both predeclared targets" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.restartRecovery?.lock !==
    "exclusive non-blocking advisory lock held for the router process lifetime" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.restartRecovery
    ?.updateOrder !==
    "temporary file sync, atomic replacement and parent-directory sync before route or session change" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.restartRecovery
    ?.startupAcceptance !==
    "three consecutive unique-writable observations matching the exact persisted route and PostgreSQL timeline before traffic acceptance" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.restartRecovery
    ?.staleControlSocket !==
    "single-link current-user-owned mode-0600 socket reclaimed only while the state lock is held" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.restartRecovery
    ?.processLoss !==
    "SIGKILL after promoted state persistence followed by recovery without caller-supplied route or timeline" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.restartRecovery
    ?.clusterReset !==
    "persisted state retired only after the disposable database cluster and router are stopped" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision
    ?.implementation !== "same-binary conformance parent with one router child" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.readiness !==
    "private state control succeeds after exact persisted route and PostgreSQL timeline reconfirmation" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision
    ?.restartBudget !== 3 ||
  JSON.stringify(
    profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision
      ?.restartBackoffSeconds,
  ) !== JSON.stringify([2, 4, 8]) ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.processLoss !==
    "SIGKILL child while the parent remains active" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision
    ?.preReadyTraffic !==
    "stable endpoint unavailable until the replacement child reports readiness" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision
    ?.childOwnership !==
    "Linux parent-death SIGKILL plus expected supervisor PID recheck before socket creation" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.parentLoss !==
    "SIGKILL supervisor terminates the owned child and makes readiness and traffic unavailable" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision
    ?.parentReplacement !==
    "recovery-only replacement reconfirms the exact persisted route and PostgreSQL timeline before readiness" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision
    ?.singletonOwnership !==
    "exclusive non-blocking supervisor-lifetime lock in the private route workspace" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.contender !==
    "rejected before state inspection or child launch without disturbing the active owner" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision
    ?.controlledTakeover !==
    "recovery-only replacement acquires released ownership after the former supervisor and child terminate" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.exhaustion !==
    "supervisor exits nonzero without serving traffic" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.manager
    ?.implementation !==
    "same-binary outer conformance parent with one singleton supervisor child" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.manager
    ?.readiness !==
    "private state control succeeds only after the managed supervisor restores the exact persisted route and PostgreSQL timeline" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.manager
    ?.restartBudget !== 3 ||
  JSON.stringify(
    profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.manager
      ?.restartBackoffSeconds,
  ) !== JSON.stringify([2, 4, 8]) ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.manager
    ?.supervisorLoss !== "SIGKILL supervisor and owned router while the manager remains active" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.manager
    ?.preReadyTraffic !==
    "stable endpoint unavailable throughout bounded replacement backoff and recovery" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.manager
    ?.childOwnership !==
    "Linux parent-death SIGKILL plus expected manager PID recheck before singleton acquisition" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.manager
    ?.replacement !==
    "automatic recovery-only supervisor replacement reconfirms exact persisted state before readiness" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.manager
    ?.parentLoss !==
    "SIGKILL manager terminates the owned supervisor and router before controlled manager replacement" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.manager
    ?.singletonOwnership !==
    "exclusive non-blocking manager-lifetime lock in the private route workspace" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.manager
    ?.contender !==
    "rejected before state inspection or supervisor launch without disturbing the active hierarchy" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.manager
    ?.controlledTakeover !==
    "recovery-only replacement acquires released ownership after the former manager and descendants terminate" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.manager
    ?.exhaustion !==
    "fourth consecutive supervisor loss exhausts the manager budget, terminates the hierarchy and leaves both service surfaces unavailable" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision?.manager
    ?.exhaustionRecovery !==
    "explicit recovery-only manager reacquires both ownership layers and reconfirms unchanged state before readiness" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.supervision
    ?.productionCertified !== false ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.longLivedPool
    ?.implementation !== "one pgxpool process across primary loss and route switch" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.longLivedPool
    ?.connections !== 3 ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.longLivedPool
    ?.outageObservation !== "at least one failed independent role probe before promotion" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.longLivedPool
    ?.reconnection !== "bounded retries through the unchanged URL" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.longLivedPool
    ?.operationTimeoutMillis !== 2000 ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.longLivedPool
    ?.phaseTimeoutSeconds !== 45 ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.longLivedPool
    ?.retryIntervalMillis !== 200 ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.longLivedPool
    ?.postSwitchAcceptance !==
    "three distinct promoted-primary sessions with rollback-scoped temporary writes" ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.longLivedPool
    ?.transactionReplay !== false ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.automaticDiscovery !==
    false ||
  profile.recoveryConformance?.clusteredFailover?.stableClientEndpoint?.productionCertified !==
    false ||
  profile.recoveryConformance?.clusteredFailover?.partitionFencing !== false ||
  profile.recoveryConformance?.clusteredFailover?.stalePrimaryRejoin?.observePhase !==
    "failover-rejoin-observe" ||
  profile.recoveryConformance?.clusteredFailover?.stalePrimaryRejoin?.fence !==
    "persistent data-volume marker enforced before PostgreSQL startup" ||
  profile.recoveryConformance?.clusteredFailover?.stalePrimaryRejoin?.fenceMode !==
    "explicit after old primary is stopped" ||
  profile.recoveryConformance?.clusteredFailover?.stalePrimaryRejoin?.staleStartExitCode !== 78 ||
  profile.recoveryConformance?.clusteredFailover?.stalePrimaryRejoin?.rewind !==
    "pg_rewind from promoted primary before fence removal" ||
  profile.recoveryConformance?.clusteredFailover?.stalePrimaryRejoin?.uncleanTarget !==
    "rejected; fresh rebuild required" ||
  profile.recoveryConformance?.clusteredFailover?.stalePrimaryRejoin?.replicationCredential !==
    "mode-0600 private data-volume passfile" ||
  profile.recoveryConformance?.clusteredFailover?.stalePrimaryRejoin?.primaryConnection !==
    "explicit promoted-service primary_conninfo with private passfile" ||
  profile.recoveryConformance?.clusteredFailover?.stalePrimaryRejoin?.rejoinRole !==
    "read-only physical standby" ||
  profile.recoveryConformance?.clusteredFailover?.stalePrimaryRejoin?.replicationBoundary !==
    "durable post-promotion binding and audit WAL flush position" ||
  profile.recoveryConformance?.clusteredFailover?.stalePrimaryRejoin?.automaticFencing !== false ||
  profile.recoveryConformance?.clusteredFailover?.inFlightCommit?.lossPhase !==
    "failover-commit-loss" ||
  profile.recoveryConformance?.clusteredFailover?.inFlightCommit?.recoveryPhase !==
    "failover-commit-recover" ||
  profile.recoveryConformance?.clusteredFailover?.inFlightCommit?.faultBoundary !==
    "COMMIT forwarded with response withheld before primary loss" ||
  JSON.stringify(
    profile.recoveryConformance?.clusteredFailover?.inFlightCommit?.acceptedOutcomes,
  ) !== JSON.stringify(["exact binding and audit", "no binding or audit"]) ||
  profile.recoveryConformance?.clusteredFailover?.inFlightCommit?.finalState !==
    "one exact binding and audit with read-only replay" ||
  profile.recoveryConformance?.ambiguousCatalogCommit?.processLoss?.phase !== "commit-loss" ||
  profile.recoveryConformance?.ambiguousCatalogCommit?.processLoss?.terminationBoundary !==
    "after PostgreSQL commit before finalizer acknowledgement" ||
  profile.recoveryConformance?.ambiguousCatalogCommit?.processLoss?.expectedExitCode !== 86 ||
  profile.recoveryConformance?.ambiguousCatalogCommit?.connectionLoss?.phase !==
    "commit-connection-loss" ||
  profile.recoveryConformance?.ambiguousCatalogCommit?.connectionLoss?.faultBoundary !==
    "PostgreSQL COMMIT completion before client observation" ||
  profile.recoveryConformance?.ambiguousCatalogCommit?.connectionLoss?.transport !==
    "bounded loopback protocol proxy" ||
  profile.recoveryConformance?.ambiguousCatalogCommit?.preDurabilityConnectionLoss?.phase !==
    "pre-commit-connection-loss" ||
  profile.recoveryConformance?.ambiguousCatalogCommit?.preDurabilityConnectionLoss
    ?.faultBoundary !== "PostgreSQL COMMIT request before server observation" ||
  profile.recoveryConformance?.ambiguousCatalogCommit?.preDurabilityConnectionLoss
    ?.expectedOutcome !== "transaction rolled back" ||
  JSON.stringify(profile.recoveryConformance?.phases) !== JSON.stringify(recoveryPhases)
) {
  fail("the joint PostgreSQL and enforcement-object recovery contract is incomplete");
}
if (
  !recoveryImplementation.includes("const ConcurrentRecoveryWorkers = 8") ||
  !/PhaseOutage\s+Phase = "outage"/.test(recoveryImplementation) ||
  !/PhaseCommitLoss\s+Phase = "commit-loss"/.test(recoveryImplementation) ||
  !/PhaseCommittedRecover\s+Phase = "committed-recover"/.test(recoveryImplementation) ||
  !/PhaseCommitConnectionLoss\s+Phase = "commit-connection-loss"/.test(recoveryImplementation) ||
  !/PhaseConnectionLossRecover\s+Phase = "connection-loss-recover"/.test(recoveryImplementation) ||
  !/PhasePreCommitConnectionLoss\s+Phase = "pre-commit-connection-loss"/.test(
    recoveryImplementation,
  ) ||
  !/PhaseRolledBackRecover\s+Phase = "rolled-back-recover"/.test(recoveryImplementation) ||
  !/PhaseFailoverRecover\s+Phase = "failover-recover"/.test(recoveryImplementation) ||
  !/PhaseFailoverCommitLoss\s+Phase = "failover-commit-loss"/.test(recoveryImplementation) ||
  !/PhaseFailoverCommitRecover\s+Phase = "failover-commit-recover"/.test(recoveryImplementation) ||
  !/PhaseFailoverRejoinObserve\s+Phase = "failover-rejoin-observe"/.test(recoveryImplementation) ||
  !recoveryImplementation.includes('"finalization-fails-closed-during-object-outage"') ||
  !recoveryImplementation.includes('"concurrent-catalog-adoption-after-restarts"') ||
  !recoveryImplementation.includes('"read-only-replay-after-ambiguous-commit"') ||
  !recoveryImplementation.includes('"commit-result-ambiguous"') ||
  !recoveryImplementation.includes('"read-only-replay-after-connection-loss"') ||
  !recoveryImplementation.includes('"pre-commit-result-ambiguous"') ||
  !recoveryImplementation.includes('"retained-object-adopted-after-rollback"') ||
  !recoveryImplementation.includes('"read-only-replay-after-rollback"') ||
  !recoveryImplementation.includes('"promoted-standby-has-replicated-fixture"') ||
  !recoveryImplementation.includes('"catalog-adopted-on-promoted-standby"') ||
  !recoveryImplementation.includes('"read-only-replay-after-failover"') ||
  !recoveryImplementation.includes('"primary-loss-during-commit-is-ambiguous"') ||
  !recoveryImplementation.includes('"atomic-catalog-outcome-observed-after-failover"') ||
  !recoveryImplementation.includes('"catalog-converged-after-in-flight-failover"') ||
  !recoveryImplementation.includes('"read-only-replay-after-in-flight-failover"') ||
  !recoveryImplementation.includes('"rewound-primary-rejoined-read-only"') ||
  !recoveryImplementation.includes('"rejoined-standby-has-converged-catalog"') ||
  !recoveryImplementation.includes('"read-only-replay-on-rejoined-standby"') ||
  !recoveryImplementation.includes('"single-audit-on-rejoined-standby"') ||
  !recoveryImplementation.includes("newArrivalBarrier(ConcurrentRecoveryWorkers)")
) {
  fail("the executable recovery suite has drifted from the scored profile");
}
if (
  !commitProxy.includes('net.Listen("tcp", "127.0.0.1:0")') ||
  !commitProxy.includes("address.IsLoopback()") ||
  !commitProxy.includes("const maxMessageBytes = 16 << 20") ||
  !commitProxy.includes("proxy.listener.Close()") ||
  !commitProxy.includes('bytes.Equal(payload, []byte("COMMIT\\x00"))') ||
  !/BeforeCommitDurability\s+FaultPoint = "before-commit-durability"/.test(commitProxy) ||
  !/AfterCommitDurability\s+FaultPoint = "after-commit-durability"/.test(commitProxy) ||
  !/DuringCommitPrimaryLoss\s+FaultPoint = "during-commit-primary-loss"/.test(commitProxy) ||
  !commitProxy.includes("close(proxy.forwarded)") ||
  !commitProxy.includes("close(proxy.dropped)")
) {
  fail("the bounded loopback PostgreSQL commit proxy has drifted from the recovery contract");
}
if (
  !recoveryCommand.includes("const commitLossExitCode = 86") ||
  !recoveryCommand.includes("func() { os.Exit(commitLossExitCode) }") ||
  !recoveryCommand.includes("recoveryconformance.RunCommitLoss") ||
  !recoveryCommand.includes("recoveryconformance.RunCommittedRecover")
) {
  fail("the recovery command has drifted from the process-loss contract");
}
if (
  !recoveryCommand.includes("pgcommitproxy.Start") ||
  !recoveryCommand.includes("recoveryconformance.RunCommitConnectionLoss") ||
  !recoveryCommand.includes("recoveryconformance.RunConnectionLossRecover") ||
  !recoveryCommand.includes("recoveryconformance.RunPreCommitConnectionLoss") ||
  !recoveryCommand.includes("recoveryconformance.RunRolledBackRecover") ||
  !recoveryCommand.includes("pgcommitproxy.BeforeCommitDurability") ||
  !recoveryCommand.includes("pgcommitproxy.AfterCommitDurability") ||
  !recoveryCommand.includes("10*time.Second")
) {
  fail("the recovery command has drifted from the commit-connection-loss contract");
}
if (
  !recoveryCommand.includes("recoveryconformance.RunFailoverRecover") ||
  !recoveryCommand.includes("verifyPromotedFixture") ||
  !recoveryCommand.includes("pg_is_in_recovery()") ||
  !recoveryCommand.includes("current_setting('transaction_read_only')")
) {
  fail("the recovery command has drifted from the promoted-standby contract");
}
if (
  !recoveryCommand.includes("recoveryconformance.RunFailoverCommitLoss") ||
  !recoveryCommand.includes("recoveryconformance.RunFailoverCommitRecover") ||
  !recoveryCommand.includes("pgcommitproxy.DuringCommitPrimaryLoss") ||
  !recoveryCommand.includes('io.WriteString(signalFile, "commit-forwarded\\n")') ||
  !recoveryCommand.includes("validPrimaryLossSignalFD")
) {
  fail("the recovery command has drifted from the in-flight failover contract");
}
if (
  !recoveryCommand.includes("recoveryconformance.RunFailoverRejoinObserve") ||
  !recoveryCommand.includes("verifyRejoinedFixture") ||
  !recoveryCommand.includes("inRecovery != expectedRecovery") ||
  !recoveryCommand.includes("transactionReadOnly != expectedReadOnly")
) {
  fail("the recovery command has drifted from the stale-primary rejoin contract");
}

if (
  profile.topology?.endpoint !== "http://127.0.0.1:8333" ||
  profile.topology?.bucket !== "dataground-conformance" ||
  profile.topology?.authentication !== "anonymous development access" ||
  profile.topology?.persistent !== false ||
  profile.topology?.restartPersistentVolume !== true ||
  JSON.stringify(profile.topology?.entrypointCapabilities) !==
    JSON.stringify(["CHOWN", "SETGID", "SETUID"])
) {
  fail("the candidate must remain disposable, loopback-only and non-authenticated");
}

if (!Array.isArray(profile.blockers) || profile.blockers.length < 6) {
  fail("production blockers are not explicit");
}

if (
  !compose.includes(backend.image) ||
  !compose.includes('"127.0.0.1:8333:8333"') ||
  !compose.includes("mem_limit: 512m") ||
  !compose.includes("pids_limit: 256") ||
  !compose.includes("cap_drop:\n      - ALL") ||
  !compose.includes("cap_add:\n      - CHOWN\n      - SETGID\n      - SETUID") ||
  !compose.includes("seaweedfs-conformance-data:/data") ||
  !compose.includes("volumes:\n  seaweedfs-conformance-data:") ||
  compose.includes("tmpfs:") ||
  compose.includes("AWS_ACCESS_KEY_ID") ||
  compose.includes("AWS_SECRET_ACCESS_KEY")
) {
  fail("Docker Compose does not match the pinned candidate profile");
}
if (
  !compose.includes("image: postgres:18.4-bookworm") ||
  !compose.includes('user: "999:999"') ||
  !compose.includes('"127.0.0.1:55432:5432"') ||
  !compose.includes("PGDATA: /var/lib/postgresql/data/pgdata") ||
  !compose.includes("postgres-conformance-data:/var/lib/postgresql") ||
  !compose.includes("\n  postgres-conformance-data:")
) {
  fail("the disposable PostgreSQL recovery dependency is not isolated consistently");
}
const postgresCompose = compose.split("\n  postgres:")[1]?.split("\n  postgres-standby:")[0] ?? "";
if (
  !postgresCompose.includes("mem_limit: 512m") ||
  !postgresCompose.includes("pids_limit: 256") ||
  !postgresCompose.includes("cap_drop:\n      - ALL") ||
  !postgresCompose.includes("no-new-privileges:true") ||
  !postgresCompose.includes("DATAGROUND_FENCE_PATH: /var/lib/postgresql/.dataground-fenced") ||
  !postgresCompose.includes("DATAGROUND_HEALTH_PASSWORD: dataground-health") ||
  !postgresCompose.includes("PGPASSFILE: /var/lib/postgresql/.dataground-replication-pass") ||
  !postgresCompose.includes("postgres-primary-entrypoint.sh") ||
  !postgresCompose.includes("wal_level=replica") ||
  !postgresCompose.includes("max_wal_senders=4") ||
  !postgresCompose.includes("max_replication_slots=4") ||
  !postgresCompose.includes("wal_keep_size=64MB") ||
  !postgresCompose.includes("wal_log_hints=on") ||
  postgresCompose.includes("cap_add:")
) {
  fail("the disposable PostgreSQL process does not retain its least-privilege boundary");
}
const standbyCompose =
  compose.split("\n  postgres-standby:")[1]?.split("\n  postgres-fence:")[0] ?? "";
if (
  !standbyCompose.includes("postgres-failover") ||
  !standbyCompose.includes("image: postgres:18.4-bookworm") ||
  !standbyCompose.includes('user: "999:999"') ||
  !standbyCompose.includes('"127.0.0.1:55433:5432"') ||
  !standbyCompose.includes("postgres-standby-conformance-data:/var/lib/postgresql") ||
  !standbyCompose.includes("postgres-standby-entrypoint.sh") ||
  !standbyCompose.includes("mem_limit: 512m") ||
  !standbyCompose.includes("pids_limit: 256") ||
  !standbyCompose.includes("cap_drop:\n      - ALL") ||
  !standbyCompose.includes("no-new-privileges:true") ||
  standbyCompose.includes("cap_add:") ||
  !compose.includes("\n  postgres-standby-conformance-data:")
) {
  fail("the disposable PostgreSQL standby is not isolated consistently");
}
const fenceCompose =
  compose.split("\n  postgres-fence:")[1]?.split("\n  postgres-rejoin:")[0] ?? "";
const rejoinCompose = compose.split("\n  postgres-rejoin:")[1]?.split("\nvolumes:")[0] ?? "";
for (const [name, service, script] of [
  ["fence", fenceCompose, "postgres-fence.sh"],
  ["rejoin", rejoinCompose, "postgres-rejoin.sh"],
]) {
  if (
    !service.includes("postgres-failover-admin") ||
    !service.includes("image: postgres:18.4-bookworm") ||
    !service.includes('user: "999:999"') ||
    !service.includes("postgres-conformance-data:/var/lib/postgresql") ||
    !service.includes("DATAGROUND_FENCE_PATH: /var/lib/postgresql/.dataground-fenced") ||
    !service.includes(script) ||
    !service.includes("cap_drop:\n      - ALL") ||
    !service.includes("no-new-privileges:true") ||
    service.includes("ports:") ||
    service.includes("cap_add:")
  ) {
    fail(`the disposable PostgreSQL ${name} service is not isolated consistently`);
  }
}
if (
  !rejoinCompose.includes(
    "DATAGROUND_REPLICATION_PASSFILE: /var/lib/postgresql/.dataground-replication-pass",
  )
) {
  fail("the PostgreSQL rejoin service does not retain its private replication credential boundary");
}
if (
  !primaryInit.includes("host replication all all scram-sha-256") ||
  !primaryInit.includes('ALTER ROLE :"replication_role" WITH REPLICATION;') ||
  !primaryInit.includes('CREATE ROLE :"health_role" WITH LOGIN PASSWORD') ||
  !primaryInit.includes("NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION") ||
  !primaryInit.includes('REVOKE CONNECT, TEMPORARY ON DATABASE :"database" FROM PUBLIC;') ||
  !primaryInit.includes('GRANT CONNECT ON DATABASE :"database" TO :"health_role";') ||
  !primaryInit.includes('ALTER ROLE :"health_role" SET search_path = pg_catalog;') ||
  !primaryInit.includes("ALTER ROLE :\"health_role\" SET statement_timeout = '2s';") ||
  !standbyEntrypoint.includes("pg_basebackup") ||
  !standbyEntrypoint.includes("--write-recovery-conf") ||
  !standbyEntrypoint.includes("application_name=dataground-standby") ||
  !standbyEntrypoint.includes('exec postgres -D "$PGDATA"') ||
  !standbyEntrypoint.includes("-c hot_standby=on") ||
  !standbyEntrypoint.includes("-c wal_level=replica") ||
  !standbyEntrypoint.includes("-c max_wal_senders=4") ||
  !standbyEntrypoint.includes("-c max_replication_slots=4") ||
  !standbyEntrypoint.includes("-c wal_keep_size=64MB") ||
  !standbyEntrypoint.includes("-c wal_log_hints=on")
) {
  fail("the physical-streaming bootstrap has drifted from the failover contract");
}
const rewindIndex = rejoinScript.indexOf("pg_rewind");
const standbySignalIndex = rejoinScript.indexOf('test -f "$PGDATA/standby.signal"');
const passfileIndex = rejoinScript.indexOf('>"$DATAGROUND_REPLICATION_PASSFILE"');
const primaryConnectionIndex = rejoinScript.indexOf("primary_conninfo = 'host=postgres-standby");
const fenceRemovalIndex = rejoinScript.indexOf('rm -f "$fence"');
if (
  !primaryEntrypoint.includes('if [ -e "$DATAGROUND_FENCE_PATH" ]') ||
  !primaryEntrypoint.includes("exit 78") ||
  !primaryEntrypoint.includes('exec /usr/local/bin/docker-entrypoint.sh "$@"') ||
  !fenceScript.includes("pg_isready --host postgres") ||
  !fenceScript.includes('>"$DATAGROUND_FENCE_PATH"') ||
  !rejoinScript.includes('if [ ! -f "$fence" ]') ||
  !rejoinScript.includes("pg_isready --host postgres") ||
  !rejoinScript.includes("SELECT NOT pg_is_in_recovery()") ||
  !rejoinScript.includes("--source-server=") ||
  !rejoinScript.includes("--write-recovery-conf") ||
  !rejoinScript.includes("--no-ensure-shutdown") ||
  !rejoinScript.includes("DATAGROUND_REPLICATION_PASSFILE") ||
  !rejoinScript.includes("postgres-standby:5432:*:%s:%s") ||
  !rejoinScript.includes("umask 077") ||
  !rejoinScript.includes("primary_conninfo = 'host=postgres-standby") ||
  !rejoinScript.includes("passfile=%s") ||
  rejoinScript.includes("password=") ||
  rewindIndex < 0 ||
  standbySignalIndex <= rewindIndex ||
  passfileIndex <= standbySignalIndex ||
  primaryConnectionIndex <= passfileIndex ||
  fenceRemovalIndex <= primaryConnectionIndex
) {
  fail("the explicit stale-primary fence and rejoin procedure has drifted");
}
if (
  !routeProxy.includes('net.ListenTCP("tcp4"') ||
  !routeProxy.includes('net.ListenUnix("unix"') ||
  !routeProxy.includes("os.Chmod(config.ControlSocket, 0o600)") ||
  !routeProxy.includes('Primary  Route = "primary"') ||
  !routeProxy.includes('Promoted Route = "promoted"') ||
  !routeProxy.includes("maxActiveConnections   = 64") ||
  !routeProxy.includes("maxControlConnections  = 8") ||
  !routeProxy.includes("selectionTimeout       = 7 * time.Second") ||
  !routeProxy.includes("probeRoundTimeout      = 2 * time.Second") ||
  !routeProxy.includes("confirmationInterval   = 200 * time.Millisecond") ||
  !routeProxy.includes("confirmationCount      = 3") ||
  !routeProxy.includes("proxy.generation++") ||
  !routeProxy.includes("connection.close()") ||
  !routeProxy.includes('"select "+strconv.FormatUint(expectedGeneration, 10)+"\\n"') ||
  !routeProxy.includes("proxy.selectWritable(expectedGeneration)") ||
  !routeProxy.includes("probeWritableRound(probeContext, routes, probe, expectedGeneration)") ||
  !routeProxy.includes("candidate.health.PromotionGeneration != expectedGeneration") ||
  !routeProxy.includes("proxy.persistSelection(selected, expectedGeneration, controlGeneration)") ||
  !routeProxy.includes("proxy.state.write(state, true)") ||
  !routeProxy.includes("promotionGeneration <= proxy.promotionGeneration") ||
  !routeProxy.includes("prepareControlSocket(config.ControlSocket)") ||
  !routeProxy.includes("confirm recovered PostgreSQL route state") ||
  !routeProxy.includes("found multiple writable targets") ||
  !routeProxy.includes("found no writable target") ||
  !routeState.includes("routeStateVersion  = 1") ||
  !routeState.includes("maxRouteStateBytes = 512") ||
  !routeState.includes("directoryInfo.Mode().Perm() != 0o700") ||
  !routeState.includes("validPrivateRouteFile") ||
  !routeState.includes("temporary.Sync()") ||
  !routeState.includes("os.Rename(temporaryPath, store.path)") ||
  !routeState.includes("directoryFile.Sync()") ||
  !routeProxy.includes('host != "127.0.0.1"') ||
  !routeCommand.includes('case "serve":') ||
  !routeCommand.includes('case "supervise":') ||
  routeCommand.includes('case "route":') ||
  !routeCommand.includes('case "select":') ||
  !routeCommand.includes('case "status":') ||
  !routeCommand.includes('case "state":') ||
  !routeCommand.includes('case "role":') ||
  !routeCommand.includes('case "pool":') ||
  !routeCommand.includes('os.Getenv("DATAGROUND_TEST_DATABASE_URL")') ||
  !routeCommand.includes('os.Getenv("DATAGROUND_ROUTER_HEALTH_DATABASE_URL")') ||
  !routeCommand.includes("candidate.Host = target") ||
  !routeCommand.includes('flags.String("state-file"') ||
  !routeCommand.includes('flags.Uint64("promotion-generation"') ||
  !routeCommand.includes("pg_split_walfile_name(pg_walfile_name(pg_current_wal_lsn()))") ||
  !poolCommand.includes("poolConformanceSize       = int32(3)") ||
  !poolCommand.includes("poolOperationTimeout      = 2 * time.Second") ||
  !poolCommand.includes("poolPhaseTimeout          = 45 * time.Second") ||
  !poolCommand.includes("poolRetryInterval         = 200 * time.Millisecond") ||
  !poolCommand.includes('"pool-primary-ready"') ||
  !poolCommand.includes('"pool-failure-observed"') ||
  !poolCommand.includes('"pool-promoted-ready"') ||
  !poolCommand.includes("config.MinConns = poolConformanceSize") ||
  !poolCommand.includes("config.MaxConns = poolConformanceSize") ||
  !poolCommand.includes("pgxpool.NewWithConfig(ctx, config)") ||
  !poolCommand.includes("current.postmasterStarted.Equal(initial.postmasterStarted)") ||
  !poolCommand.includes("verifyPoolWrite(ctx, connection)") ||
  !poolCommand.includes("transaction.Rollback(ctx)") ||
  routeCommand.includes("password") ||
  poolCommand.includes("password")
) {
  fail("the stable PostgreSQL client endpoint has drifted from its bounded routing contract");
}
if (
  !routeSupervisor.includes(
    "RestartBackoffs:  []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}",
  ) ||
  !routeSupervisor.includes("ReadinessTimeout: 12 * time.Second") ||
  !routeSupervisor.includes("ShutdownTimeout:  3 * time.Second") ||
  !routeSupervisor.includes("command := exec.Command(executable, arguments...)") ||
  !routeSupervisor.includes("command.Stdout = io.Discard") ||
  !routeSupervisor.includes("command.Stderr = io.Discard") ||
  !routeSupervisor.includes("configureRouteChildOwnership(command)") ||
  !routeSupervisor.includes("acquireRouteSupervisorOwnership(config)") ||
  !routeSupervisor.includes("ownership.Close()") ||
  !routeSupervisor.includes("dependencies.StateStatus") ||
  !routeSupervisor.includes("routeChildArguments(config, initializing, os.Getpid())") ||
  !routeSupervisor.includes('"--mode", "serve"') ||
  !routeSupervisor.includes('"--supervisor-pid", strconv.Itoa(supervisorPID)') ||
  !routeSupervisor.includes("if initializing == stateExists") ||
  !routeSupervisor.includes("if attempt >= len(policy.RestartBackoffs)") ||
  !routeSupervisor.includes("stopRouteChild(child, childExit, policy.ShutdownTimeout)") ||
  !routeSupervisor.includes('const routerReadyState = "router-ready"') ||
  !routeSupervisor.includes('const routerRestartScheduledState = "router-restart-scheduled"') ||
  routeSupervisor.includes("password")
) {
  fail("the PostgreSQL route supervisor has drifted from its bounded conformance contract");
}
if (
  !routeManager.includes(
    "RestartBackoffs:  []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}",
  ) ||
  !routeManager.includes("ReadinessTimeout: 12 * time.Second") ||
  !routeManager.includes("ShutdownTimeout:  5 * time.Second") ||
  !routeManager.includes("acquireRouteManagerOwnership(config)") ||
  !routeManager.includes('errors.New("release PostgreSQL route manager ownership")') ||
  !routeManager.includes("command := exec.Command(executable, arguments...)") ||
  !routeManager.includes("command.Stdout = io.Discard") ||
  !routeManager.includes("command.Stderr = io.Discard") ||
  !routeManager.includes("configureRouteChildOwnership(command)") ||
  !routeManager.includes("runBoundedConformanceSupervisor(") ||
  !routeManager.includes("supervisorChildArguments(config, initializing, os.Getpid())") ||
  !routeManager.includes('"--mode", "supervise"') ||
  !routeManager.includes('"--manager-pid", strconv.Itoa(managerPID)') ||
  !routeManager.includes('const supervisorReadyState = "supervisor-ready"') ||
  !routeManager.includes(
    'const supervisorRestartScheduledState = "supervisor-restart-scheduled"',
  ) ||
  routeManager.includes("password")
) {
  fail("the PostgreSQL route manager has drifted from its bounded replacement contract");
}
if (
  !routeOwnershipLinux.includes("syscall.O_NOFOLLOW") ||
  !routeOwnershipLinux.includes("syscall.O_CLOEXEC") ||
  !routeOwnershipLinux.includes("syscall.LOCK_EX|syscall.LOCK_NB") ||
  !routeOwnershipLinux.includes('routeManagerOwnershipSuffix = ".manager.lock"') ||
  !routeOwnershipLinux.includes("acquireRouteManagerOwnership(") ||
  !routeOwnershipLinux.includes("routeManagerOwnershipPath(config.StateFile)") ||
  !routeOwnershipLinux.includes("info.Mode().Perm() != 0o600") ||
  !routeOwnershipLinux.includes("stat.Nlink != 1") ||
  !routeOwnershipLinux.includes("stat.Uid != uint32(os.Geteuid())")
) {
  fail("the PostgreSQL route ownership boundaries have drifted");
}
if (
  !routeCommand.includes('flags.Int("supervisor-pid"') ||
  !routeCommand.includes('flags.Int("manager-pid"') ||
  !routeCommand.includes("validateRouteChildOwnership(*supervisorPID)") ||
  !routeCommand.includes("validateRouteChildOwnership(*managerPID)") ||
  !routeChildOwnership.includes("os.Getppid() != expectedSupervisorPID") ||
  !routeChildOwnershipLinux.includes("Pdeathsig: syscall.SIGKILL")
) {
  fail("the PostgreSQL route child has drifted from its supervisor ownership contract");
}
if (
  !packageManifest.scripts?.verify?.includes("pnpm run s3:profile:check") ||
  !workflow.includes("pnpm verify") ||
  !workflow.includes("dataground-s3-conformance") ||
  !workflow.includes("dataground-enforcement-recovery-conformance") ||
  !workflow.includes("--phase outage") ||
  !workflow.includes("stop seaweedfs") ||
  !workflow.includes("rm --force seaweedfs") ||
  !workflow.includes("up --detach seaweedfs") ||
  !workflow.includes("restart postgres") ||
  !workflow.includes("--phase commit-loss") ||
  !workflow.includes('"$status" -ne 86') ||
  !workflow.includes("--phase committed-recover") ||
  !workflow.includes("--phase commit-connection-loss") ||
  !workflow.includes("--phase connection-loss-recover") ||
  !workflow.includes("--phase pre-commit-connection-loss") ||
  !workflow.includes("--phase rolled-back-recover") ||
  !workflow.includes("postgres-failover-conformance") ||
  !workflow.includes("--profile postgres-failover") ||
  !workflow.includes(
    "DATAGROUND_ROUTED_DATABASE_URL: postgres://dataground:dataground@127.0.0.1:55431/dataground_conformance?sslmode=disable",
  ) ||
  !workflow.includes(
    "DATAGROUND_ROUTER_HEALTH_DATABASE_URL: postgres://dataground_health:dataground-health@127.0.0.1:55431/dataground_conformance?sslmode=disable",
  ) ||
  workflow.includes("DATAGROUND_PRIMARY_DATABASE_URL") ||
  workflow.includes("DATAGROUND_PROMOTED_DATABASE_URL") ||
  !workflow.includes('go build -o "$router" ./cmd/dataground-postgres-route-conformance') ||
  !workflow.includes("--listen-address 127.0.0.1:55431") ||
  !workflow.includes("--primary-target 127.0.0.1:55432") ||
  !workflow.includes("--promoted-target 127.0.0.1:55433") ||
  !workflow.includes('--control-socket "$control"') ||
  !workflow.includes('--state-file "$state"') ||
  !workflow.includes('install -d -m 700 "$workspace"') ||
  !workflow.includes("--promotion-generation") ||
  !workflow.includes("postgres-primary-generation") ||
  !workflow.includes("postgres-promoted-generation") ||
  !workflow.includes("Reject concurrent route supervisor contender") ||
  !workflow.includes("concurrent route supervisor acquired active ownership") ||
  !workflow.includes("route supervisor contender disturbed the active ownership boundary") ||
  !workflow.includes("state.supervisor.lock") ||
  !workflow.includes("Reject concurrent route manager contender") ||
  !workflow.includes("route manager contender disturbed the active ownership boundary") ||
  !workflow.includes("state.manager.lock") ||
  !workflow.includes('timeout --signal=KILL 5s "$router"') ||
  !workflow.includes('"PostgreSQL route manager failed"') ||
  !workflow.includes("stable database endpoint accepted a stale promotion generation") ||
  !workflow.includes("stale promotion generation changed the stable database route") ||
  !workflow.includes("stable database endpoint accepted a stale in-flight promotion generation") ||
  !workflow.includes("stale in-flight promotion generation changed the stable database route") ||
  !workflow.includes("promoted_generation <= initial_generation") ||
  !workflow.includes("stable database endpoint selected a target before promotion") ||
  !workflow.includes("failed health selection changed the stable database route") ||
  !workflow.includes('selected=$("$router" \\') ||
  !workflow.includes('--mode pool >"$pool_stdout" 2>"$pool_stderr"') ||
  !workflow.includes("'pool-primary-ready'") ||
  !workflow.includes("'pool-failure-observed'") ||
  !workflow.includes("'pool-promoted-ready'") ||
  !workflow.includes(
    "expected_pool_states=$'pool-primary-ready\\npool-failure-observed\\npool-promoted-ready'",
  ) ||
  !workflow.includes("long-lived database pool did not reconnect through the stable endpoint") ||
  !workflow.includes("Supervise stable endpoint after router process loss") ||
  !workflow.includes("--mode supervise") ||
  !workflow.includes("--mode manage") ||
  !workflow.includes('manager_pid=$(cat "$RUNNER_TEMP/postgres-route-manager.pid")') ||
  !workflow.includes('supervisor_pid=$(pgrep --parent "$manager_pid" || true)') ||
  !workflow.includes('child_pid=$(pgrep --parent "$supervisor_pid" || true)') ||
  !workflow.includes('kill -KILL "$child_pid"') ||
  !workflow.includes("route manager reported readiness before router recovery") ||
  !workflow.includes("stable database endpoint remained usable after router process loss") ||
  !workflow.includes("Automatically replace supervisor after process loss") ||
  !workflow.includes('kill -KILL "$supervisor_pid"') ||
  !workflow.includes(
    "expected_manager_states=$'supervisor-ready\\nsupervisor-restart-scheduled\\nsupervisor-ready'",
  ) ||
  !workflow.includes("supervisor process loss did not enter bounded replacement backoff") ||
  !workflow.includes("route manager reported readiness before supervisor replacement") ||
  !workflow.includes("route manager did not restore the exact persisted supervisor boundary") ||
  !workflow.includes("Replace manager after parent process loss") ||
  !workflow.includes('kill -KILL "$manager_pid"') ||
  !workflow.includes('rm --force "$RUNNER_TEMP/postgres-route-manager.pid"') ||
  !workflow.includes("managed route processes outlived the manager ownership boundary") ||
  !workflow.includes("manager process loss left route readiness available") ||
  !workflow.includes("manager process loss left the stable database endpoint available") ||
  !workflow.includes("replacement manager did not recover the exact ownership boundary") ||
  !workflow.includes("Exhaust route manager restart budget and recover") ||
  !workflow.includes(
    "expected_exhausted_states=$'supervisor-ready\\nsupervisor-restart-scheduled\\nsupervisor-ready\\nsupervisor-restart-scheduled\\nsupervisor-ready\\nsupervisor-restart-scheduled\\nsupervisor-ready'",
  ) ||
  !workflow.includes("route manager did not enter the expected restart backoff") ||
  !workflow.includes("exhausted route manager left a managed route process") ||
  !workflow.includes("exhausted route manager left route readiness available") ||
  !workflow.includes("exhausted route manager left the stable database endpoint available") ||
  !workflow.includes("exhausted route manager emitted unexpected lifecycle output") ||
  !workflow.includes("state_ownership_before=$(stat --format '%d:%i' \"$state.lock\")") ||
  !workflow.includes("postgres-route-manager-exhaustion-recovery.stdout") ||
  !workflow.includes("recovered manager did not restore the exact exhaustion boundary") ||
  !workflow.includes("replacement database endpoint manager emitted unexpected output") ||
  !workflow.includes("stable database endpoint did not recover its exact private state") ||
  !workflow.includes("stat --format '%a' \"$state.lock\"") ||
  !workflow.includes('rm "$state"') ||
  !workflow.includes("stable database endpoint remained usable before route change") ||
  !workflow.includes(
    "DATAGROUND_TEST_DATABASE_URL: $" + "{{ env.DATAGROUND_ROUTED_DATABASE_URL }}",
  ) ||
  !workflow.includes("pg_current_wal_lsn()") ||
  !workflow.includes("pg_last_wal_replay_lsn()") ||
  !workflow.includes("stop postgres") ||
  !workflow.includes("pg_ctl promote") ||
  !workflow.includes("--phase failover-recover") ||
  !workflow.includes("--phase failover-commit-loss") ||
  !workflow.includes("--primary-loss-signal-fd 3") ||
  !workflow.includes('state" != "commit-forwarded') ||
  !workflow.includes("--phase failover-commit-recover") ||
  !workflow.includes("--profile postgres-failover-admin run --rm --no-deps postgres-fence") ||
  !workflow.includes('"$exit_code" -ne 78') ||
  !workflow.includes("--profile postgres-failover-admin run --rm --no-deps postgres-rejoin") ||
  !workflow.includes("dataground_stale_write_probe") ||
  !workflow.includes("pg_current_wal_flush_lsn()") ||
  !workflow.includes("pg_stat_replication") ||
  !workflow.includes("rejoin check failed: target-state=") ||
  !workflow.includes("' boundary-replayed='") ||
  !workflow.includes("' replay-paused='") ||
  !workflow.includes("--phase failover-rejoin-observe") ||
  !workflow.includes("DATAGROUND_TEST_DATABASE_URL") ||
  !workflow.includes("deploy/storage/seaweedfs-conformance.yml up --detach") ||
  !workflow.includes("deploy/storage/seaweedfs-conformance.yml down --volumes")
) {
  fail("CI does not enforce the pinned live conformance profile");
}

console.log("S3 enforcement-object development profile is internally consistent.");
