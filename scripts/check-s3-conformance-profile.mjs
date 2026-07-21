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
  profile.recoveryConformance?.clusteredFailover?.primaryLoss !==
    "primary container stopped before promotion" ||
  profile.recoveryConformance?.clusteredFailover?.promotion !== "explicit pg_ctl promotion" ||
  profile.recoveryConformance?.clusteredFailover?.automaticElection !== false ||
  profile.recoveryConformance?.clusteredFailover?.clientEndpointFailover !== false ||
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
  profile.recoveryConformance?.clusteredFailover?.stalePrimaryRejoin?.rejoinRole !==
    "read-only physical standby" ||
  profile.recoveryConformance?.clusteredFailover?.stalePrimaryRejoin?.replicationBoundary !==
    "post-promotion binding and audit WAL position" ||
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
  !postgresCompose.includes("PGPASSFILE: /var/lib/postgresql/.dataground-replication-pass") ||
  !postgresCompose.includes("postgres-primary-entrypoint.sh") ||
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
  !standbyEntrypoint.includes("pg_basebackup") ||
  !standbyEntrypoint.includes("--write-recovery-conf") ||
  !standbyEntrypoint.includes("application_name=dataground-standby") ||
  !standbyEntrypoint.includes('exec postgres -D "$PGDATA" -c hot_standby=on')
) {
  fail("the physical-streaming bootstrap has drifted from the failover contract");
}
const rewindIndex = rejoinScript.indexOf("pg_rewind");
const standbySignalIndex = rejoinScript.indexOf('test -f "$PGDATA/standby.signal"');
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
  rejoinScript.includes("password=") ||
  rewindIndex < 0 ||
  standbySignalIndex <= rewindIndex ||
  fenceRemovalIndex <= standbySignalIndex
) {
  fail("the explicit stale-primary fence and rejoin procedure has drifted");
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
  !workflow.includes("--phase failover-rejoin-observe") ||
  !workflow.includes("DATAGROUND_TEST_DATABASE_URL") ||
  !workflow.includes("deploy/storage/seaweedfs-conformance.yml up --detach") ||
  !workflow.includes("deploy/storage/seaweedfs-conformance.yml down --volumes")
) {
  fail("CI does not enforce the pinned live conformance profile");
}

console.log("S3 enforcement-object development profile is internally consistent.");
