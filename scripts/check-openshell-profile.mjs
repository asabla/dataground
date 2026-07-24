import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const profilePath = resolve(root, "deploy/openshell/development-profile.json");
const profile = JSON.parse(await readFile(profilePath, "utf8"));

const failures = [];
const fail = (message) => failures.push(message);
const digestPattern = /@sha256:[a-f0-9]{64}$/;
const expectedCanarySurfaces = [
  "sandbox-process",
  "sandbox-environment",
  "sandbox-filesystem",
  "provider-arguments",
  "gateway-logs",
  "sandbox-logs",
  "runtime-errors",
];

if (profile.schemaVersion !== "dataground.dev.openshell-profile/v1") {
  fail("unexpected development profile schema version");
}
if (profile.status !== "blocked" || profile.productionCertifiable !== false) {
  fail("the incomplete development profile must remain blocked and non-certifiable");
}
if (
  profile.source?.rosetta?.candidateCommit !== "320158f1e4a4eea378d82c1527f4a7af5fb9855b" ||
  profile.source?.rosetta?.declaredCompilerVersion !== "1.0.0" ||
  profile.source?.rosetta?.catalogVersion !== "rosetta/v1" ||
  profile.source?.rosetta?.openShellTargetContract !== "rosetta/openshell-policy-v1" ||
  !profile.source?.rosetta?.releaseStatus?.includes("tagged and certified release required")
) {
  fail("the Rosetta candidate contract evidence is missing or no longer blocked");
}
if (
  profile.topology?.driver !== "docker" ||
  profile.topology?.gatewayEndpoint !== "http://127.0.0.1:8080"
) {
  fail("the initial topology must remain the loopback Docker profile");
}
for (const [name, reference] of Object.entries(profile.artifacts ?? {})) {
  if (typeof reference !== "string" || !digestPattern.test(reference)) {
    fail(`${name} is not pinned to an OCI sha256 digest`);
  }
}
if (profile.enforcement?.mode !== "deny-all") {
  fail("the checked-in enforcement fixture must remain deny-all");
}

const fixturePath = resolve(root, profile.enforcement.fixture);
const fixture = await readFile(fixturePath);
const actualFixtureDigest = createHash("sha256").update(fixture).digest("hex");
if (actualFixtureDigest !== profile.enforcement.sha256) {
  fail("the enforcement fixture does not match its recorded upstream digest");
}
if (profile.runtime?.nativeInterface !== "app-server JSONL over stdio") {
  fail("the first Codex integration must use the native app-server stdio interface");
}
const credentialEvidenceContract = profile.providerProfileEvidence?.contract;
if (
  credentialEvidenceContract?.schema !==
    "deploy/openshell/credential-non-exposure-evidence.schema.json" ||
  credentialEvidenceContract?.schemaVersion !==
    "dataground.dev.openshell-credential-non-exposure-evidence/v1" ||
  credentialEvidenceContract?.verifier !==
    "pnpm openshell:credential-evidence:check <evidence.json>" ||
  credentialEvidenceContract?.verifierIdentity?.name !== "dataground-openshell-canary" ||
  credentialEvidenceContract?.verifierIdentity?.version !== "1.0.0" ||
  credentialEvidenceContract?.status !== "contract verified; live evidence required"
) {
  fail("the credential non-exposure evidence contract is missing or unblocked");
}
const credentialEvidenceSchema = JSON.parse(
  await readFile(
    resolve(root, "deploy/openshell/credential-non-exposure-evidence.schema.json"),
    "utf8",
  ),
);
const evidenceRunProperties = credentialEvidenceSchema?.properties?.run?.properties;
if (
  credentialEvidenceSchema?.properties?.schemaVersion?.const !==
    credentialEvidenceContract?.schemaVersion ||
  evidenceRunProperties?.startedAt?.format !== "date-time" ||
  evidenceRunProperties?.startedAt?.pattern !== "Z$" ||
  evidenceRunProperties?.finishedAt?.format !== "date-time" ||
  evidenceRunProperties?.finishedAt?.pattern !== "Z$" ||
  evidenceRunProperties?.verifier?.properties?.name?.const !==
    credentialEvidenceContract?.verifierIdentity?.name ||
  evidenceRunProperties?.verifier?.properties?.version?.const !==
    credentialEvidenceContract?.verifierIdentity?.version
) {
  fail("the credential evidence schema does not match the verifier identity contract");
}
const canaryScanner = credentialEvidenceContract?.scanner;
if (
  canaryScanner?.command !== "go run ./cmd/dataground-openshell-canary-scan" ||
  canaryScanner?.schema !== "deploy/openshell/credential-canary-scan.schema.json" ||
  canaryScanner?.schemaVersion !== "dataground.dev.openshell-canary-scan/v1" ||
  canaryScanner?.canaryFormat !==
    "dataground-canary-v1:<43-character unpadded base64url entropy>" ||
  canaryScanner?.observationWindow !==
    "scanner-owned UTC RFC3339 interval within the evidence run" ||
  canaryScanner?.status !== "commitment-only scanner verified; live harness required"
) {
  fail("the commitment-only credential canary scanner contract is missing or unblocked");
}
const surfaceMaxBytes = canaryScanner?.surfaceMaxBytes;
if (
  typeof surfaceMaxBytes !== "object" ||
  surfaceMaxBytes === null ||
  JSON.stringify(Object.keys(surfaceMaxBytes).sort()) !==
    JSON.stringify([...expectedCanarySurfaces].sort()) ||
  Object.values(surfaceMaxBytes).some(
    (limit) => !Number.isSafeInteger(limit) || limit <= 0 || limit > 268_435_456,
  )
) {
  fail("the credential canary scanner must define one bounded input limit per surface");
}
const canaryScannerSchema = JSON.parse(
  await readFile(resolve(root, canaryScanner?.schema ?? ""), "utf8"),
);
if (
  canaryScannerSchema?.properties?.schemaVersion?.const !== canaryScanner?.schemaVersion ||
  JSON.stringify([...(canaryScannerSchema?.properties?.surface?.enum ?? [])].sort()) !==
    JSON.stringify([...expectedCanarySurfaces].sort()) ||
  !canaryScannerSchema?.required?.includes("canaryCommitment") ||
  canaryScannerSchema?.properties?.canaryCommitment?.pattern !== "^sha256:[a-f0-9]{64}$" ||
  !canaryScannerSchema?.required?.includes("inputLimitBytes") ||
  canaryScannerSchema?.properties?.inputLimitBytes?.minimum !== 1 ||
  canaryScannerSchema?.properties?.inputLimitBytes?.maximum !== 268_435_456 ||
  !canaryScannerSchema?.required?.includes("startedAt") ||
  !canaryScannerSchema?.required?.includes("finishedAt") ||
  canaryScannerSchema?.properties?.startedAt?.format !== "date-time" ||
  canaryScannerSchema?.properties?.startedAt?.pattern !== "Z$" ||
  canaryScannerSchema?.properties?.finishedAt?.format !== "date-time" ||
  canaryScannerSchema?.properties?.finishedAt?.pattern !== "Z$"
) {
  fail("the credential canary scanner report schema does not match the development profile");
}
if (
  profile.runtime?.schemaEvidence?.file !== "codex_app_server_protocol.v2.schemas.json" ||
  !/^[a-f0-9]{64}$/.test(profile.runtime?.schemaEvidence?.canonicalSHA256 ?? "") ||
  !profile.runtime?.schemaEvidence?.canonicalization
) {
  fail("the pinned Codex schema must have canonical, reproducible evidence");
}

const compose = await readFile(resolve(root, "deploy/openshell/docker-compose.yml"), "utf8");
const gatewayConfig = await readFile(resolve(root, "deploy/openshell/gateway.toml"), "utf8");
if (!compose.includes(profile.artifacts.gateway) || !compose.includes('"127.0.0.1:8080:8080"')) {
  fail("Docker Compose does not match the pinned loopback gateway profile");
}
if (
  !gatewayConfig.includes(profile.artifacts.supervisor) ||
  !gatewayConfig.includes(profile.artifacts.sandbox)
) {
  fail("gateway configuration does not match the pinned supervisor and sandbox images");
}
if (
  compose.includes(":latest") ||
  gatewayConfig.includes(":latest") ||
  gatewayConfig.includes("enable_bind_mounts = true")
) {
  fail("mutable images or unsafe sandbox bind mounts are present in the local topology");
}
if (!Array.isArray(profile.blockers) || profile.blockers.length === 0) {
  fail("incomplete live certification blockers must be explicit");
}

if (failures.length > 0) {
  console.error(failures.map((message) => `- ${message}`).join("\n"));
  process.exitCode = 1;
} else {
  console.log("OpenShell development profile is internally consistent.");
}
