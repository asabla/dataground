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
};
if (
  profile.recoveryConformance?.reportSchema !== "dataground.enforcement-recovery-conformance/v1" ||
  profile.recoveryConformance?.database !== "PostgreSQL 18.4 disposable test dependency" ||
  profile.recoveryConformance?.freshRunnerProcesses !== true ||
  profile.recoveryConformance?.postgresServerRestart !== true ||
  profile.recoveryConformance?.objectBackendOutage !== true ||
  profile.recoveryConformance?.objectContainerRecreated !== true ||
  profile.recoveryConformance?.concurrentRecoveryWorkers !== 8 ||
  JSON.stringify(profile.recoveryConformance?.phases) !== JSON.stringify(recoveryPhases)
) {
  fail("the joint PostgreSQL and enforcement-object recovery contract is incomplete");
}
if (
  !recoverySuite.includes("const ConcurrentRecoveryWorkers = 8") ||
  !recoverySuite.includes('PhaseOutage  Phase = "outage"') ||
  !recoverySuite.includes('"finalization-fails-closed-during-object-outage"') ||
  !recoverySuite.includes('"concurrent-catalog-adoption-after-restarts"') ||
  !recoverySuite.includes("newArrivalBarrier(ConcurrentRecoveryWorkers)")
) {
  fail("the executable recovery suite has drifted from the scored profile");
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
const postgresCompose = compose.split("\n  postgres:")[1] ?? "";
if (
  !postgresCompose.includes("mem_limit: 512m") ||
  !postgresCompose.includes("pids_limit: 256") ||
  !postgresCompose.includes("cap_drop:\n      - ALL") ||
  !postgresCompose.includes("no-new-privileges:true") ||
  postgresCompose.includes("cap_add:")
) {
  fail("the disposable PostgreSQL process does not retain its least-privilege boundary");
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
  !workflow.includes("DATAGROUND_TEST_DATABASE_URL") ||
  !workflow.includes("deploy/storage/seaweedfs-conformance.yml up --detach") ||
  !workflow.includes("deploy/storage/seaweedfs-conformance.yml down --volumes")
) {
  fail("CI does not enforce the pinned live conformance profile");
}

console.log("S3 enforcement-object development profile is internally consistent.");
