import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const profilePath = resolve(root, "deploy/openshell/development-profile.json");
const profile = JSON.parse(await readFile(profilePath, "utf8"));

const failures = [];
const fail = (message) => failures.push(message);
const digestPattern = /@sha256:[a-f0-9]{64}$/;
const expectedSurfaceResourceKinds = {
  "sandbox-process": "sandbox",
  "sandbox-environment": "sandbox",
  "sandbox-filesystem": "sandbox",
  "provider-arguments": "provider",
  "gateway-logs": "gateway",
  "sandbox-logs": "sandbox",
  "runtime-errors": "runtime",
};
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
  fail("the development profile must remain production-blocked and non-certifiable");
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
  profile.topology?.gatewayEndpoint !== "http://127.0.0.1:8080" ||
  profile.topology?.healthEndpoint !== "http://127.0.0.1:8081/readyz" ||
  profile.topology?.gatewayState !==
    "unique run-derived same-path bind directory removed before evidence release" ||
  profile.topology?.gatewayAuthentication !==
    "fresh run-scoped Ed25519 signing keys mounted read-only and removed with the frozen topology" ||
  profile.topology?.gatewayUserAuthentication !==
    "unauthenticated CLI calls accepted only on the dedicated loopback plaintext gateway; sandbox supervisors require run-scoped JWTs" ||
  profile.topology?.gatewayNetwork !==
    "gateway container shares the Docker host network namespace so the driver-owned sandbox bridge listener is host-reachable while control and health remain loopback-only"
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
    "dataground.dev.openshell-credential-non-exposure-evidence/v3" ||
  credentialEvidenceContract?.verifier !==
    "pnpm openshell:credential-evidence:check <evidence.json>" ||
  JSON.stringify(Object.keys(credentialEvidenceContract?.verifierIdentity ?? {}).sort()) !==
    JSON.stringify(["name", "version"]) ||
  credentialEvidenceContract?.verifierIdentity?.name !== "dataground-openshell-canary" ||
  credentialEvidenceContract?.verifierIdentity?.version !== "3.0.0" ||
  credentialEvidenceContract?.status !== "coverage-qualified v3 evidence incorporated and verified"
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
const evidenceCheckSchema = credentialEvidenceSchema?.properties?.checks?.items;
if (
  credentialEvidenceSchema?.properties?.schemaVersion?.const !==
    credentialEvidenceContract?.schemaVersion ||
  !credentialEvidenceSchema?.properties?.profile?.required?.includes("composeSHA256") ||
  !credentialEvidenceSchema?.properties?.profile?.required?.includes("gatewayConfigSHA256") ||
  credentialEvidenceSchema?.properties?.profile?.properties?.composeSHA256?.$ref !==
    "#/$defs/sha256" ||
  credentialEvidenceSchema?.properties?.profile?.properties?.gatewayConfigSHA256?.$ref !==
    "#/$defs/sha256" ||
  evidenceRunProperties?.id?.pattern !== "^[a-f0-9]{32}$" ||
  !evidenceCheckSchema?.required?.includes("inputCommitment") ||
  evidenceCheckSchema?.properties?.inputCommitment?.pattern !== "^sha256:[a-f0-9]{64}$" ||
  JSON.stringify(Object.keys(evidenceRunProperties?.resources?.properties ?? {}).sort()) !==
    JSON.stringify(["gateway", "provider", "runtime", "sandbox", "workspace"]) ||
  evidenceRunProperties?.resources?.properties?.workspace?.$ref !== "#/$defs/workspaceName" ||
  credentialEvidenceSchema?.$defs?.workspaceName?.pattern !== "^dg-canary-[a-f0-9]{32}$" ||
  credentialEvidenceSchema?.properties?.cleanup?.properties?.sandbox?.$ref !==
    "#/$defs/cleanupReceipt" ||
  evidenceRunProperties?.resources?.properties?.provider?.$ref !== "#/$defs/providerName" ||
  credentialEvidenceSchema?.$defs?.providerName?.pattern !== "^dg-canary-provider-[a-f0-9]{32}$" ||
  credentialEvidenceSchema?.properties?.cleanup?.properties?.providerBinding?.$ref !==
    "#/$defs/providerCleanupReceipt" ||
  credentialEvidenceSchema?.$defs?.providerCleanupReceipt?.properties?.name?.$ref !==
    "#/$defs/providerName" ||
  credentialEvidenceSchema?.properties?.cleanup?.properties?.workspace?.$ref !==
    "#/$defs/workspaceCleanupReceipt" ||
  credentialEvidenceSchema?.$defs?.workspaceCleanupReceipt?.properties?.name?.$ref !==
    "#/$defs/workspaceName" ||
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

const acceptedCredentialEvidence = credentialEvidenceContract?.acceptedEvidence;
const acceptedCredentialEvidenceKeys = [
  "artifactArchiveDigest",
  "artifactID",
  "file",
  "headCommit",
  "runID",
  "sha256",
  "workflow",
  "workflowRunID",
];
if (
  JSON.stringify(Object.keys(acceptedCredentialEvidence ?? {}).sort()) !==
    JSON.stringify(acceptedCredentialEvidenceKeys) ||
  acceptedCredentialEvidence?.file !==
    "deploy/openshell/evidence/openshell-credential-non-exposure-v3.json" ||
  !/^[a-f0-9]{64}$/.test(acceptedCredentialEvidence?.sha256 ?? "") ||
  !/^[a-f0-9]{32}$/.test(acceptedCredentialEvidence?.runID ?? "") ||
  acceptedCredentialEvidence?.workflow !== ".github/workflows/openshell-credential-evidence.yml" ||
  !Number.isSafeInteger(acceptedCredentialEvidence?.workflowRunID) ||
  !/^[a-f0-9]{40}$/.test(acceptedCredentialEvidence?.headCommit ?? "") ||
  !Number.isSafeInteger(acceptedCredentialEvidence?.artifactID) ||
  !/^sha256:[a-f0-9]{64}$/.test(acceptedCredentialEvidence?.artifactArchiveDigest ?? "")
) {
  fail("the accepted credential evidence provenance is incomplete");
}

let acceptedCredentialEvidenceBytes;
let acceptedCredentialEvidenceRecord;
try {
  acceptedCredentialEvidenceBytes = await readFile(
    resolve(root, acceptedCredentialEvidence?.file ?? ""),
  );
  acceptedCredentialEvidenceRecord = JSON.parse(acceptedCredentialEvidenceBytes);
} catch {
  fail("the accepted credential evidence record is unavailable or malformed");
}
if (
  acceptedCredentialEvidenceBytes &&
  createHash("sha256").update(acceptedCredentialEvidenceBytes).digest("hex") !==
    acceptedCredentialEvidence?.sha256
) {
  fail("the accepted credential evidence record does not match its recorded digest");
}
if (
  acceptedCredentialEvidenceRecord &&
  (acceptedCredentialEvidenceRecord.schemaVersion !== credentialEvidenceContract?.schemaVersion ||
    acceptedCredentialEvidenceRecord.run?.id !== acceptedCredentialEvidence?.runID ||
    JSON.stringify(acceptedCredentialEvidenceRecord.run?.verifier) !==
      JSON.stringify(credentialEvidenceContract?.verifierIdentity) ||
    acceptedCredentialEvidenceRecord.result !== "passed" ||
    acceptedCredentialEvidenceRecord.profile?.openshellCommit !== profile.source.openshell.commit ||
    acceptedCredentialEvidenceRecord.profile?.gatewayImage !== profile.artifacts.gateway ||
    acceptedCredentialEvidenceRecord.profile?.supervisorImage !== profile.artifacts.supervisor ||
    acceptedCredentialEvidenceRecord.profile?.sandboxImage !== profile.artifacts.sandbox ||
    acceptedCredentialEvidenceRecord.profile?.providerProfileSourceSHA256 !==
      profile.providerProfileEvidence.codex.sha256 ||
    acceptedCredentialEvidenceRecord.profile?.runtimeVersion !== profile.runtime.version ||
    acceptedCredentialEvidenceRecord.profile?.gatewayEndpoint !==
      profile.topology.gatewayEndpoint ||
    acceptedCredentialEvidenceRecord.profile?.driver !== profile.topology.driver ||
    acceptedCredentialEvidenceRecord.profile?.composeSHA256 !== profile.topology.composeSHA256 ||
    acceptedCredentialEvidenceRecord.profile?.gatewayConfigSHA256 !==
      profile.topology.gatewayConfigSHA256)
) {
  fail("the accepted credential evidence record is not bound to the checked profile");
}
const canaryScanner = credentialEvidenceContract?.scanner;
const canaryScannerCommandSource = await readFile(
  resolve(root, "cmd/dataground-openshell-canary-scan/main.go"),
  "utf8",
);
const canaryScannerReportSource = await readFile(
  resolve(root, "internal/security/canaryscan/report.go"),
  "utf8",
);
const canaryCollectorSource = await readFile(
  resolve(root, "internal/security/canarycollect/collector.go"),
  "utf8",
);
const canarySourceAdapter = await readFile(
  resolve(root, "internal/security/canarysource/source.go"),
  "utf8",
);
const canaryOpenShellSource = await readFile(
  resolve(root, "internal/execution/openshell/credential_evidence_source.go"),
  "utf8",
);
const canaryDockerSource = await readFile(
  resolve(root, "internal/security/canarydocker/source.go"),
  "utf8",
);
const canaryRuntimeSource = await readFile(
  resolve(root, "internal/security/canaryruntime/source.go"),
  "utf8",
);
const canaryEvidenceSource = await readFile(
  resolve(root, "internal/security/canaryevidence/evidence.go"),
  "utf8",
);
const canaryWorkspaceSource = await readFile(
  resolve(root, "internal/security/canaryworkspace/workspace.go"),
  "utf8",
);
const canarySandboxSource = await readFile(
  resolve(root, "internal/security/canarysandbox/cleanup.go"),
  "utf8",
);
const canaryProviderSource = await readFile(
  resolve(root, "internal/security/canaryprovider/cleanup.go"),
  "utf8",
);
const canaryProviderProvisionSource = await readFile(
  resolve(root, "internal/security/canaryprovider/provision.go"),
  "utf8",
);
const canaryHarnessSource = await readFile(
  resolve(root, "internal/security/canaryharness/harness.go"),
  "utf8",
);
const canaryProfileSource = await readFile(
  resolve(root, "internal/security/canaryprofile/profile.go"),
  "utf8",
);
const canaryLauncherSource = await readFile(
  resolve(root, "internal/security/canarylauncher/launcher.go"),
  "utf8",
);
const canaryLauncherHostSource = await readFile(
  resolve(root, "internal/security/canarylauncher/host.go"),
  "utf8",
);
const canaryLauncherTopologySource = await readFile(
  resolve(root, "internal/security/canarylauncher/topology.go"),
  "utf8",
);
const openShellRunnerSource = await readFile(
  resolve(root, "internal/execution/openshell/runner.go"),
  "utf8",
);
const canaryLauncherCommandSource = await readFile(
  resolve(root, "cmd/dataground-openshell-canary/main.go"),
  "utf8",
);
const providerBindingSource = await readFile(
  resolve(root, "internal/execution/openshell/provider_binding.go"),
  "utf8",
);
const providerBindingCreateSource = await readFile(
  resolve(root, "internal/execution/openshell/provider_binding_create.go"),
  "utf8",
);
if (
  canaryScanner?.command !== "go run ./cmd/dataground-openshell-canary-scan" ||
  canaryScanner?.schema !== "deploy/openshell/credential-canary-scan.schema.json" ||
  canaryScanner?.schemaVersion !== "dataground.dev.openshell-canary-scan/v1" ||
  canaryScanner?.reportAssembly !== "internal/security/canaryscan.ScanReport" ||
  !canaryScannerCommandSource.includes("canaryscan.ScanReport(") ||
  !canaryScannerCommandSource.includes("output.HasMatches()") ||
  !canaryScannerReportSource.includes("func ValidateReportConfig(") ||
  !canaryScannerReportSource.includes("func ScanReport(") ||
  !canaryScannerReportSource.includes("func (report Report) MarshalJSON(") ||
  canaryScannerReportSource.includes("\nfunc Scan(") ||
  canaryScanner?.canaryFormat !==
    "dataground-canary-v1:<43-character unpadded base64url entropy>" ||
  canaryScanner?.observationWindow !==
    "scanner-owned UTC RFC3339 interval within the evidence run" ||
  canaryScanner?.inputCommitment !==
    "sha256 of four-byte big-endian length-prefixed UTF-8 domain, run ID, surface, resource kind, resource name, then the 32-byte input sha256" ||
  canaryScanner?.inputCommitmentDomain !== "dataground.openshell-canary-input/v1" ||
  !canaryScannerReportSource.includes('"dataground.openshell-canary-input/v1"') ||
  canaryScanner?.runIDFormat !== "32-character lowercase hexadecimal nonce" ||
  canaryScanner?.resourceNameFormat !== "portable lowercase identifier" ||
  JSON.stringify(canaryScanner?.surfaceResourceKinds) !==
    JSON.stringify(expectedSurfaceResourceKinds) ||
  canaryScanner?.status !== "commitment-only scanner and accepted live Docker execution verified"
) {
  fail("the commitment-only credential canary scanner contract is missing or unblocked");
}
const canaryCollector = credentialEvidenceContract?.collector;
if (
  canaryCollector?.assembly !== "internal/security/canarycollect.Collect" ||
  canaryCollector?.sourceContract !==
    "one-shot io.ReadCloser acquired from a run-owned live resource" ||
  canaryCollector?.reportHandoff !== "direct to internal/security/canaryscan.ScanReport" ||
  canaryCollector?.limitOwnership !== "collector-owned exact profile limits" ||
  JSON.stringify(canaryCollector?.sourceOrder) !== JSON.stringify(expectedCanarySurfaces) ||
  canaryCollector?.status !==
    "collection, acquisition, launcher wiring, and accepted live Docker execution verified" ||
  !canaryCollectorSource.includes("func Collect(") ||
  !canaryCollectorSource.includes("canaryscan.ValidateReportConfig(") ||
  !canaryCollectorSource.includes("canaryscan.ScanReport(") ||
  !canaryCollectorSource.includes("source.Close()") ||
  !canaryCollectorSource.includes("if !collection.complete") ||
  !canaryCollectorSource.includes("ErrCollectionIncomplete") ||
  canaryCollectorSource.includes("os/exec") ||
  canaryCollectorSource.includes("docker") ||
  canaryCollectorSource.includes("openshell")
) {
  fail("the credential source collection boundary is missing or claims live acquisition");
}
const canarySourceAcquisition = credentialEvidenceContract?.evidenceRun?.sourceAcquisition;
if (
  canarySourceAcquisition?.assembly !== "internal/security/canarysource.New" ||
  canarySourceAcquisition?.binding !==
    "exact evidence run, canary commitment, and portable gateway, sandbox, provider, and runtime names" ||
  canarySourceAcquisition?.sourcePorts !== "typed OpenShell, Docker, and runtime streams" ||
  canarySourceAcquisition?.handoff !==
    "single-use canonical collection with direct scanner handoff" ||
  canarySourceAcquisition?.serialization !== "forbidden" ||
  canarySourceAcquisition?.status !==
    "all seven source backends, launcher wiring, and accepted live Docker execution verified" ||
  canarySourceAcquisition?.openShell?.assembly !==
    "internal/execution/openshell.NewCredentialEvidenceSources" ||
  canarySourceAcquisition?.openShell?.binding !==
    "exact persisted execution, private native sandbox, and gateway endpoint" ||
  canarySourceAcquisition?.openShell?.transport !==
    "streaming pinned OpenShell sandbox exec argument vectors without a shell" ||
  canarySourceAcquisition?.openShell?.versionCheck !== "OpenShell 0.0.86 before acquisition" ||
  JSON.stringify(canarySourceAcquisition?.openShell?.surfaces) !==
    JSON.stringify([
      "sandbox-process",
      "sandbox-environment",
      "sandbox-filesystem",
      "sandbox-logs",
    ]) ||
  canarySourceAcquisition?.openShell?.serialization !== "forbidden" ||
  canarySourceAcquisition?.openShell?.status !==
    "OpenShell sandbox source backend, launcher wiring, and accepted live Docker execution verified" ||
  canarySourceAcquisition?.docker?.assembly !== "internal/security/canarydocker.New" ||
  canarySourceAcquisition?.docker?.binding !==
    "exact running gateway container ID, digest-pinned image, Compose project and service, and run, gateway, and provider evidence labels" ||
  canarySourceAcquisition?.docker?.transport !==
    "streaming Docker inspect argument vectors and complete timestamped stdout and stderr logs without a shell" ||
  JSON.stringify(canarySourceAcquisition?.docker?.surfaces) !==
    JSON.stringify(["provider-arguments", "gateway-logs"]) ||
  canarySourceAcquisition?.docker?.serialization !== "forbidden" ||
  canarySourceAcquisition?.docker?.status !==
    "Docker host source backend, launcher wiring, and accepted live Docker execution verified" ||
  canarySourceAcquisition?.runtime?.assembly !== "internal/security/canaryruntime.New" ||
  canarySourceAcquisition?.runtime?.binding !==
    "same exact execution.RuntimeSession passed to the native runtime adapter, evidence run, and portable runtime name" ||
  canarySourceAcquisition?.runtime?.capture !==
    "bounded in-memory capture of complete native stderr before one scanner handoff" ||
  canarySourceAcquisition?.runtime?.maxBytes !== 16_777_216 ||
  canarySourceAcquisition?.runtime?.zeroization !==
    "captured bytes cleared after scanner close, unusable handoff, or explicit discard" ||
  canarySourceAcquisition?.runtime?.serialization !== "forbidden" ||
  canarySourceAcquisition?.runtime?.status !==
    "runtime-error source backend, launcher wiring, and accepted live Docker execution verified" ||
  !canarySourceAdapter.includes("func New(") ||
  !canarySourceAdapter.includes("func (adapter *Adapter) ValidateBinding(") ||
  !canarySourceAdapter.includes("func (adapter *Adapter) Collect(") ||
  !canarySourceAdapter.includes("canarycollect.Collect(") ||
  !canarySourceAdapter.includes("ErrAlreadyStarted") ||
  !canarySourceAdapter.includes("func (Adapter) MarshalJSON(") ||
  !canarySourceAdapter.includes("OpenSandboxProcess(") ||
  !canarySourceAdapter.includes("OpenProviderArguments(") ||
  !canarySourceAdapter.includes("OpenRuntimeErrors(") ||
  canarySourceAdapter.includes("os/exec") ||
  canarySourceAdapter.includes('"docker"') ||
  canarySourceAdapter.includes('"openshell"')
) {
  fail("the credential source adapter is missing or claims a concrete live backend");
}
if (
  !canaryOpenShellSource.includes("func (provider *Provider) NewCredentialEvidenceSources(") ||
  !canaryOpenShellSource.includes('credentialEvidenceOpenShellVersion  = "0.0.86"') ||
  !canaryOpenShellSource.includes("provider.Check(ctx)") ||
  !canaryOpenShellSource.includes("type ExecEvidenceStreamRunner struct{}") ||
  !canaryOpenShellSource.includes("exec.CommandContext(") ||
  !canaryOpenShellSource.includes('"sandbox", "exec", "--name"') ||
  !canaryOpenShellSource.includes('"--no-tty", "--"') ||
  !canaryOpenShellSource.includes('"find", "/proc"') ||
  !canaryOpenShellSource.includes('"-name", "cmdline"') ||
  !canaryOpenShellSource.includes('"-name", "environ"') ||
  !canaryOpenShellSource.includes('credentialEvidenceFilesystemProgram = `"use strict";') ||
  !canaryOpenShellSource.includes('const excludedDirectories = new Set(["/proc", "/sys"]);') ||
  !canaryOpenShellSource.includes('const requiredFiles = ["/etc/os-release"];') ||
  !canaryOpenShellSource.includes("const buffer = Buffer.alloc(64 * 1024);") ||
  !canaryOpenShellSource.includes("excludedDirectories.has(candidate)") ||
  !canaryOpenShellSource.includes("copyFile(file, true)") ||
  !canaryOpenShellSource.includes("if (copiedBytes === 0)") ||
  canaryOpenShellSource.includes("rootDevice") ||
  canaryOpenShellSource.includes("stat.dev ===") ||
  !canaryOpenShellSource.includes("fs.writeSync(1, buffer") ||
  !canaryOpenShellSource.includes('"node", "-e", credentialEvidenceFilesystemProgram') ||
  canaryOpenShellSource.includes("child_process") ||
  canaryOpenShellSource.includes("process.env") ||
  canaryOpenShellSource.includes("process.argv") ||
  !canaryOpenShellSource.includes('"find", "/var/log"') ||
  !canaryOpenShellSource.includes('"-name", "openshell.*.log"') ||
  !canaryOpenShellSource.includes('entry.Execution.State != "ready"') ||
  !canaryOpenShellSource.includes("func (CredentialEvidenceSources) MarshalJSON(") ||
  canaryOpenShellSource.includes('"sh", "-c"')
) {
  fail("the concrete OpenShell credential source backend is missing or unsafe");
}
if (
  !canaryDockerSource.includes("func New(") ||
  !canaryDockerSource.includes("exec.LookPath(") ||
  !canaryDockerSource.includes("exec.CommandContext(") ||
  !canaryDockerSource.includes('"inspect", "--type", "container", "--format"') ||
  !canaryDockerSource.includes('"logs", "--timestamps"') ||
  !canaryDockerSource.includes("com.docker.compose.service") ||
  !canaryDockerSource.includes("dataground.dev/credential-evidence-run") ||
  !canaryDockerSource.includes("dataground.dev/credential-evidence-gateway") ||
  !canaryDockerSource.includes("dataground.dev/credential-evidence-provider") ||
  !canaryDockerSource.includes("func (Sources) MarshalJSON(") ||
  canaryDockerSource.includes('"sh", "-c"')
) {
  fail("the concrete Docker credential source backend is missing or unsafe");
}
if (
  !canaryRuntimeSource.includes("func New(") ||
  !canaryRuntimeSource.includes("maxRuntimeErrorBytes = 16 << 20") ||
  !canaryRuntimeSource.includes("execution.RuntimeSession") ||
  !canaryRuntimeSource.includes("func (sources *Sources) Errors(") ||
  !canaryRuntimeSource.includes("func (sources *Sources) OpenRuntimeErrors(") ||
  !canaryRuntimeSource.includes("func (sources *Sources) Discard(") ||
  !canaryRuntimeSource.includes("clear(state.capture)") ||
  !canaryRuntimeSource.includes("func (Sources) MarshalJSON(") ||
  canaryRuntimeSource.includes("os/exec")
) {
  fail("the concrete runtime credential source backend is missing or unsafe");
}
const canaryEvidenceRun = credentialEvidenceContract?.evidenceRun;
const compactCanaryEvidenceSource = canaryEvidenceSource.replaceAll(/\s/g, "");
const compactCanaryWorkspaceSource = canaryWorkspaceSource.replaceAll(/\s/g, "");
const compactCanaryHarnessSource = canaryHarnessSource.replaceAll(/\s/g, "");
if (
  canaryEvidenceRun?.assembly !== "internal/security/canaryevidence.Run" ||
  canaryEvidenceRun?.profileOwnership !==
    "checked profile snapshot, topology digests, and dataground-openshell-canary/3.0.0 verifier identity" ||
  JSON.stringify(canaryEvidenceRun?.cleanupOrder) !==
    JSON.stringify(["sandbox", "provider", "workspace"]) ||
  canaryEvidenceRun?.cleanupContext !== "cancellation-independent run-owned cleanup" ||
  canaryEvidenceRun?.serialization !== "sealed until collection and every cleanup succeeds" ||
  canaryEvidenceRun?.workspaceCleanup?.assembly !== "internal/security/canaryworkspace.Open" ||
  canaryEvidenceRun?.workspaceCleanup?.name !== "dg-canary-<runID>" ||
  canaryEvidenceRun?.workspaceCleanup?.parent !== "pre-existing owner-only mode-0700 directory" ||
  canaryEvidenceRun?.workspaceCleanup?.serialization !== "forbidden" ||
  canaryEvidenceRun?.workspaceCleanup?.status !==
    "workspace lifecycle, launcher wiring, and accepted live Docker execution verified" ||
  canaryEvidenceRun?.sandboxCleanup?.assembly !== "internal/security/canarysandbox.New" ||
  canaryEvidenceRun?.sandboxCleanup?.resource !==
    "exact DataGround execution returned by the OpenShell provider" ||
  canaryEvidenceRun?.sandboxCleanup?.verification !==
    "terminate then require a timestamped exact terminal observation" ||
  canaryEvidenceRun?.sandboxCleanup?.serialization !== "forbidden" ||
  canaryEvidenceRun?.sandboxCleanup?.status !==
    "sandbox cleanup, launcher wiring, and accepted live Docker execution verified" ||
  canaryEvidenceRun?.providerProvisioning?.assembly !==
    "internal/security/canaryprovider.Provision" ||
  canaryEvidenceRun?.providerProvisioning?.name !== "dg-canary-provider-<runID>" ||
  canaryEvidenceRun?.providerProvisioning?.profile !== "codex" ||
  JSON.stringify(canaryEvidenceRun?.providerProvisioning?.credentials) !==
    JSON.stringify(["access_token", "refresh_token", "account_id", "id_token"]) ||
  canaryEvidenceRun?.providerProvisioning?.canary !==
    "32-byte cryptographic entropy encoded as the structured canary and cleared after provider creation" ||
  canaryEvidenceRun?.providerProvisioning?.transport !==
    "pinned OpenShell 0.0.86 provider create with bare credential keys and an isolated child environment" ||
  canaryEvidenceRun?.providerProvisioning?.recovery !==
    "require pre-create absence then credential-safe exact identity observation on the dedicated evidence gateway" ||
  canaryEvidenceRun?.providerProvisioning?.serialization !== "forbidden" ||
  canaryEvidenceRun?.providerProvisioning?.status !==
    "provider provisioning, closed composition, launcher wiring, and accepted live Docker execution verified" ||
  canaryEvidenceRun?.providerCleanup?.assembly !== "internal/security/canaryprovider.New" ||
  canaryEvidenceRun?.providerCleanup?.name !== "dg-canary-provider-<runID>" ||
  canaryEvidenceRun?.providerCleanup?.resource !==
    "exact OpenShell provider ID, name, resource version, isolation domain, and gateway" ||
  canaryEvidenceRun?.providerCleanup?.verification !==
    "observe exact binding, delete, then require timestamped absence" ||
  canaryEvidenceRun?.providerCleanup?.exclusivity !==
    "dedicated evidence gateway with no concurrent provider mutation" ||
  canaryEvidenceRun?.providerCleanup?.serialization !== "forbidden" ||
  canaryEvidenceRun?.providerCleanup?.status !==
    "provider cleanup, launcher wiring, and accepted live Docker execution verified" ||
  canaryEvidenceRun?.composition?.assembly !== "internal/security/canaryharness.New" ||
  canaryEvidenceRun?.composition?.binding !==
    "exact run-derived portable names, provisioned provider binding and commitment, ready execution, verifier workspace, and concrete OpenShell, Docker, and runtime backends" ||
  canaryEvidenceRun?.composition?.components !==
    "repository-owned source adapter plus sandbox, provider, and workspace cleanup adapters" ||
  canaryEvidenceRun?.composition?.singleUse !== "shared irreversible state across harness copies" ||
  canaryEvidenceRun?.composition?.runtimeRecovery !==
    "discard rejected or unused runtime capture during composition and after every attempted run" ||
  canaryEvidenceRun?.composition?.runtimeTransition !==
    "close the exact wrapped session once after the first six live surfaces and before runtime-error acquisition" ||
  canaryEvidenceRun?.composition?.serialization !== "forbidden" ||
  canaryEvidenceRun?.composition?.status !==
    "closed run, launcher composition, and accepted live Docker execution verified" ||
  canaryEvidenceRun?.launcher?.assembly !== "internal/security/canarylauncher.Run" ||
  canaryEvidenceRun?.launcher?.command !==
    "go run ./cmd/dataground-openshell-canary --workspace-root <owner-only-mode-0700-directory>" ||
  canaryEvidenceRun?.launcher?.topologyBinding !==
    "exact checked SHA-256 of Docker Compose and gateway configuration frozen into a private run-owned directory before mutation" ||
  canaryEvidenceRun?.launcher?.gatewayLifecycle !==
    "fresh run-derived Compose project and named volume with container and volume absence verification" ||
  canaryEvidenceRun?.launcher?.gatewayAuthentication !==
    "fresh non-serializable Ed25519 keypair and identifier generated inside the private topology workspace, mounted read-only, and removed before evidence release" ||
  canaryEvidenceRun?.launcher?.environment !==
    "allowlisted Docker and OpenShell child environments; provider canary isolated separately" ||
  canaryEvidenceRun?.launcher?.recovery !==
    "persist the deterministic sandbox route before native create, then clean sandbox, provider, verifier workspace, gateway, volume, and frozen topology" ||
  canaryEvidenceRun?.launcher?.composition !==
    "repository-owned provider provisioning, provider-bound sandbox, wrapped Codex client, seven source backends, and closed harness" ||
  canaryEvidenceRun?.launcher?.output !==
    "evidence JSON on stdout only after complete run cleanup and gateway teardown" ||
  canaryEvidenceRun?.launcher?.serialization !== "configuration forbidden" ||
  canaryEvidenceRun?.launcher?.status !==
    "launcher implementation and accepted live Docker execution verified" ||
  canaryEvidenceRun?.status !==
    "provider provisioning, run, source lifecycle, cleanup, closed composition, launcher, and accepted live Docker execution verified" ||
  !canaryEvidenceSource.includes("func Run(") ||
  !canaryEvidenceSource.includes("config.Sources.ValidateBinding(") ||
  !canaryEvidenceSource.includes("config.Sources.Collect(") ||
  !canaryEvidenceSource.includes("context.WithoutCancel(") ||
  !canaryEvidenceSource.includes("if !result.complete") ||
  !canaryEvidenceSource.includes("ErrRunIncomplete") ||
  !compactCanaryEvidenceSource.includes('Result:"passed"') ||
  !compactCanaryEvidenceSource.includes('cleanupStatusRemoved="removed"') ||
  !compactCanaryEvidenceSource.includes('workspaceNamePrefix="dg-canary-"') ||
  !compactCanaryEvidenceSource.includes(
    "config.Resources.Workspace!=workspaceNamePrefix+config.RunID",
  ) ||
  !compactCanaryEvidenceSource.includes('providerNamePrefix="dg-canary-provider-"') ||
  !compactCanaryEvidenceSource.includes(
    "config.Resources.Provider!=providerNamePrefix+config.RunID",
  ) ||
  canaryEvidenceSource.includes("os/exec") ||
  !canaryEvidenceSource.includes("canaryprofile.Current()") ||
  !canaryLauncherSource.includes("func Run(") ||
  !canaryLauncherSource.includes("readVerifiedFile(") ||
  !canaryLauncherSource.includes("openTopologyWorkspace(") ||
  !canaryLauncherSource.includes("topology.ComposePath()") ||
  !canaryLauncherSource.includes("topology.JWTPath()") ||
  !canaryLauncherSource.includes("state.topology.Cleanup(ctx)") ||
  !canaryLauncherSource.includes("openshell.ExecRunner{Environment: openShellEnvironment()}") ||
  !canaryLauncherSource.includes("canaryprovider.Provision(") ||
  !canaryLauncherSource.includes("provider.Create(") ||
  !canaryLauncherSource.includes("provider.StartRuntime(") ||
  !canaryLauncherSource.includes("codex.New(runtimeSources)") ||
  !canaryLauncherSource.includes("canaryharness.New(") ||
  !canaryLauncherSource.includes("harness.Run(ctx)") ||
  !canaryLauncherSource.includes("func (Config) MarshalJSON(") ||
  !canaryLauncherHostSource.includes('"compose"') ||
  !canaryLauncherHostSource.includes('"down"') ||
  !canaryLauncherHostSource.includes('"--volumes"') ||
  !canaryLauncherHostSource.includes('"ps"') ||
  !canaryLauncherHostSource.includes('"--all"') ||
  !canaryLauncherHostSource.includes('"volume"') ||
  !canaryLauncherHostSource.includes('"label=com.docker.compose.project="') ||
  !canaryLauncherHostSource.includes("openShellEnvironment()") ||
  canaryLauncherHostSource.includes("os.Environ()") ||
  !canaryLauncherTopologySource.includes("func openTopologyWorkspace(") ||
  !canaryLauncherTopologySource.includes("func writeGatewayJWT(") ||
  !canaryLauncherTopologySource.includes("ed25519.GenerateKey(rand.Reader)") ||
  !canaryLauncherTopologySource.includes("x509.MarshalPKCS8PrivateKey(") ||
  !canaryLauncherTopologySource.includes("os.O_CREATE|os.O_EXCL|os.O_WRONLY") ||
  !canaryLauncherTopologySource.includes("os.SameFile(") ||
  !canaryLauncherTopologySource.includes("func (workspace *topologyWorkspace) Cleanup(") ||
  !openShellRunnerSource.includes("Environment []string") ||
  !openShellRunnerSource.includes("command.Env = append(") ||
  !canaryLauncherHostSource.includes("DATAGROUND_CREDENTIAL_EVIDENCE_RUN_ID") ||
  !canaryLauncherHostSource.includes("DATAGROUND_CREDENTIAL_EVIDENCE_GATEWAY") ||
  !canaryLauncherHostSource.includes("DATAGROUND_CREDENTIAL_EVIDENCE_PROVIDER") ||
  !canaryLauncherHostSource.includes("DATAGROUND_CREDENTIAL_EVIDENCE_JWT_PATH") ||
  !canaryLauncherCommandSource.includes("canarylauncher.Run(") ||
  canaryLauncherSource.includes('"sh", "-c"') ||
  canaryLauncherHostSource.includes('"sh", "-c"') ||
  !canaryWorkspaceSource.includes("func Open(") ||
  !canaryWorkspaceSource.includes("func (workspace *Workspace) Cleanup(") ||
  !canaryWorkspaceSource.includes("os.Mkdir(") ||
  !canaryWorkspaceSource.includes("os.Remove(") ||
  !canaryWorkspaceSource.includes("parent.Sync()") ||
  !compactCanaryWorkspaceSource.includes('constworkspacePrefix="dg-canary-"') ||
  !canaryWorkspaceSource.includes("regexp.MustCompile(`^[a-f0-9]{32}$`)") ||
  !canaryWorkspaceSource.includes('request.ResourceKind != "workspace"') ||
  canaryWorkspaceSource.includes("os.MkdirAll(") ||
  canaryWorkspaceSource.includes("os.RemoveAll(") ||
  !canarySandboxSource.includes("func New(") ||
  !canarySandboxSource.includes("func (adapter *Adapter) Cleanup(") ||
  !canarySandboxSource.includes("provider.Terminate(") ||
  !canarySandboxSource.includes("provider.Observe(") ||
  !canarySandboxSource.includes('observation.State != "terminated"') ||
  !canarySandboxSource.includes("observation.ObservedAt.IsZero()") ||
  !canarySandboxSource.includes("func (Adapter) MarshalJSON(") ||
  !canaryProviderSource.includes("func New(") ||
  !canaryProviderSource.includes("func (adapter *Adapter) Cleanup(") ||
  !canaryProviderSource.includes("manager.ObserveProviderBinding(") ||
  !canaryProviderSource.includes("manager.DeleteProviderBinding(") ||
  !canaryProviderSource.includes('"dg-canary-provider-"') ||
  !canaryProviderSource.includes("func (Adapter) MarshalJSON(") ||
  canaryProviderSource.includes("os/exec") ||
  !canaryProviderProvisionSource.includes("func Provision(") ||
  !canaryProviderProvisionSource.includes("rand.Reader") ||
  !canaryProviderProvisionSource.includes("io.ReadFull(") ||
  !canaryProviderProvisionSource.includes('"dataground-canary-v1:"') ||
  !canaryProviderProvisionSource.includes("clear(canary)") ||
  !canaryProviderProvisionSource.includes("CreateCredentialEvidenceProvider(") ||
  !canaryProviderProvisionSource.includes("func (Provisioned) MarshalJSON(") ||
  canaryProviderProvisionSource.includes("os/exec") ||
  !canaryHarnessSource.includes("func NamesForRun(") ||
  !canaryHarnessSource.includes("func New(") ||
  !canaryHarnessSource.includes("func (harness *Harness) Run(") ||
  !canaryHarnessSource.includes("canarysandbox.New(") ||
  !canaryHarnessSource.includes("canaryprovider.New(") ||
  !canaryHarnessSource.includes("canarysource.New(") ||
  !canaryHarnessSource.includes("canaryevidence.Run") ||
  !canaryHarnessSource.includes("boundary.runtime.Close()") ||
  !canaryHarnessSource.includes("config.Runtime.Discard()") ||
  !canaryHarnessSource.includes("state.runtime.Discard()") ||
  !canaryHarnessSource.includes('config.Execution.State != "ready"') ||
  !canaryHarnessSource.includes("func (Harness) MarshalJSON(") ||
  !compactCanaryHarnessSource.includes('gatewayNamePrefix="dg-canary-gateway-"') ||
  !compactCanaryHarnessSource.includes('runtimeNamePrefix="dg-canary-runtime-"') ||
  canaryHarnessSource.includes("os/exec") ||
  !providerBindingCreateSource.includes(
    "func (provider *Provider) CreateCredentialEvidenceProvider(",
  ) ||
  !providerBindingCreateSource.includes("provider.Check(ctx)") ||
  !providerBindingCreateSource.includes("observeProviderBindingName(") ||
  !providerBindingCreateSource.includes('"provider",') ||
  !providerBindingCreateSource.includes('"create",') ||
  !providerBindingCreateSource.includes('"--credential", key') ||
  !providerBindingCreateSource.includes("command.Env = make(") ||
  !providerBindingCreateSource.includes("command.Stderr = io.Discard") ||
  !providerBindingCreateSource.includes("context.WithoutCancel(ctx)") ||
  !providerBindingCreateSource.includes("credentialProviderRecoveryTimeout") ||
  providerBindingCreateSource.includes('"sh", "-c"') ||
  !providerBindingSource.includes("func (provider *Provider) ObserveProviderBinding(") ||
  !providerBindingSource.includes("providerBindingMaxOutputBytes") ||
  providerBindingSource.includes("ConfigKeys") ||
  !providerBindingSource.includes("func (provider *Provider) DeleteProviderBinding(") ||
  !providerBindingSource.includes('"provider",') ||
  !providerBindingSource.includes('"list",') ||
  !providerBindingSource.includes('"delete", ref.Name') ||
  !providerBindingSource.includes('"json",') ||
  !providerBindingSource.includes("ResourceVersion") ||
  !providerBindingSource.includes("execution.ErrStateConflict")
) {
  fail("the credential evidence run boundary is missing or claims live execution");
}
const evidenceProfileBindings = [
  credentialEvidenceContract.schemaVersion,
  profile.source.openshell.commit,
  profile.artifacts.gateway,
  profile.artifacts.supervisor,
  profile.artifacts.sandbox,
  profile.providerProfileEvidence.codex.sha256,
  profile.runtime.version,
  profile.topology.gatewayEndpoint,
  profile.topology.driver,
  profile.topology.composeSHA256,
  profile.topology.gatewayConfigSHA256,
  credentialEvidenceContract.verifierIdentity.name,
  credentialEvidenceContract.verifierIdentity.version,
];
if (
  evidenceProfileBindings.some(
    (binding) => !canaryProfileSource.includes(JSON.stringify(binding)),
  ) ||
  !canaryEvidenceSource.includes("canaryprofile.Current()") ||
  !canaryLauncherSource.includes("canaryprofile.ComposeSHA256") ||
  !canaryLauncherSource.includes("canaryprofile.GatewayConfigSHA256") ||
  !canaryProfileSource.includes(JSON.stringify(profile.topology.healthEndpoint)) ||
  !canaryLauncherHostSource.includes("canaryprofile.HealthEndpoint")
) {
  fail("the credential evidence run does not own the checked profile identity");
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
const compactCanaryCollectorSource = canaryCollectorSource.replaceAll(/\s/g, "");
if (
  Object.entries(surfaceMaxBytes).some(
    ([surface, limit]) =>
      !compactCanaryCollectorSource.includes(
        `"${surface}":${limit === 268_435_456 ? "256<<20" : "16<<20"},`,
      ),
  )
) {
  fail("the credential collector limits do not match the development profile");
}
const surfaceMinimumInspectedBytes = canaryScanner?.surfaceMinimumInspectedBytes;
if (
  JSON.stringify(canaryScanner?.filesystemExcludedRoots) !== JSON.stringify(["/proc", "/sys"]) ||
  JSON.stringify(canaryScanner?.filesystemRequiredFiles) !== JSON.stringify(["/etc/os-release"]) ||
  canaryScanner?.filesystemTraversal !==
    "all mounted filesystems except exact virtual kernel roots; regular files only; no symlink traversal" ||
  typeof surfaceMinimumInspectedBytes !== "object" ||
  surfaceMinimumInspectedBytes === null ||
  JSON.stringify(Object.keys(surfaceMinimumInspectedBytes).sort()) !==
    JSON.stringify([...expectedCanarySurfaces].sort()) ||
  Object.entries(surfaceMinimumInspectedBytes).some(
    ([surface, minimum]) =>
      !Number.isSafeInteger(minimum) || minimum < 0 || minimum > surfaceMaxBytes[surface],
  )
) {
  fail("the credential scanner coverage contract must be explicit and bounded per surface");
}
const canaryScannerSchema = JSON.parse(
  await readFile(resolve(root, canaryScanner?.schema ?? ""), "utf8"),
);
if (
  canaryScannerSchema?.properties?.schemaVersion?.const !== canaryScanner?.schemaVersion ||
  JSON.stringify([...(canaryScannerSchema?.properties?.surface?.enum ?? [])].sort()) !==
    JSON.stringify([...expectedCanarySurfaces].sort()) ||
  !canaryScannerSchema?.required?.includes("runID") ||
  canaryScannerSchema?.properties?.runID?.pattern !== "^[a-f0-9]{32}$" ||
  !canaryScannerSchema?.required?.includes("resource") ||
  canaryScannerSchema?.properties?.resource?.properties?.name?.pattern !==
    "^[a-z0-9][a-z0-9._-]{0,127}$" ||
  JSON.stringify(
    [...(canaryScannerSchema?.properties?.resource?.properties?.kind?.enum ?? [])].sort(),
  ) !== JSON.stringify(["gateway", "provider", "runtime", "sandbox"]) ||
  !canaryScannerSchema?.required?.includes("canaryCommitment") ||
  canaryScannerSchema?.properties?.canaryCommitment?.pattern !== "^sha256:[a-f0-9]{64}$" ||
  !canaryScannerSchema?.required?.includes("inputCommitment") ||
  canaryScannerSchema?.properties?.inputCommitment?.pattern !== "^sha256:[a-f0-9]{64}$" ||
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
if (
  createHash("sha256").update(compose).digest("hex") !== profile.topology.composeSHA256 ||
  createHash("sha256").update(gatewayConfig).digest("hex") !== profile.topology.gatewayConfigSHA256
) {
  fail("the Docker topology does not match its recorded content digests");
}
if (!compose.includes(profile.artifacts.gateway) || !compose.includes("network_mode: host")) {
  fail("Docker Compose does not match the pinned loopback gateway profile");
}
if (
  !compose.includes(
    `dataground.dev/credential-evidence-run: "\${DATAGROUND_CREDENTIAL_EVIDENCE_RUN_ID:-}"`,
  ) ||
  !compose.includes(
    `dataground.dev/credential-evidence-gateway: "\${DATAGROUND_CREDENTIAL_EVIDENCE_GATEWAY:-}"`,
  ) ||
  !compose.includes(
    `dataground.dev/credential-evidence-provider: "\${DATAGROUND_CREDENTIAL_EVIDENCE_PROVIDER:-}"`,
  )
) {
  fail("Docker Compose is missing the exact run-bound credential evidence labels");
}
if (
  !gatewayConfig.includes(profile.artifacts.supervisor) ||
  !gatewayConfig.includes(profile.artifacts.sandbox) ||
  !gatewayConfig.includes('bind_address = "127.0.0.1:8080"') ||
  !gatewayConfig.includes('health_bind_address = "127.0.0.1:8081"') ||
  !gatewayConfig.includes("[openshell.gateway.auth]") ||
  !gatewayConfig.includes("allow_unauthenticated_users = true") ||
  !gatewayConfig.includes("[openshell.gateway.gateway_jwt]") ||
  !gatewayConfig.includes(
    'signing_key_path = "/run/dataground-credential-evidence/gateway-jwt/signing.pem"',
  ) ||
  !gatewayConfig.includes(
    'public_key_path = "/run/dataground-credential-evidence/gateway-jwt/public.pem"',
  ) ||
  !gatewayConfig.includes('kid_path = "/run/dataground-credential-evidence/gateway-jwt/kid"') ||
  !gatewayConfig.includes('gateway_id = "dataground-credential-evidence"') ||
  !gatewayConfig.includes("ttl_secs = 0")
) {
  fail("gateway configuration does not match the pinned supervisor and sandbox images");
}
if (
  !compose.includes(`source: "\${DATAGROUND_CREDENTIAL_EVIDENCE_STATE_PATH:?required}"`) ||
  !compose.includes(`target: "\${DATAGROUND_CREDENTIAL_EVIDENCE_STATE_PATH:?required}"`) ||
  !compose.includes(`source: "\${DATAGROUND_CREDENTIAL_EVIDENCE_JWT_PATH:?required}"`) ||
  !compose.includes("target: /run/dataground-credential-evidence/gateway-jwt") ||
  !compose.includes(
    `user: "\${DATAGROUND_CREDENTIAL_EVIDENCE_UID:?required}:\${DATAGROUND_CREDENTIAL_EVIDENCE_GID:?required}"`,
  ) ||
  !compose.includes(`- "\${DATAGROUND_CREDENTIAL_EVIDENCE_DOCKER_GID:?required}"`) ||
  compose.includes('"127.0.0.1:8080:8080"') ||
  compose.includes('"127.0.0.1:8081:8081"') ||
  compose.includes("gateway-state:/var/lib/openshell") ||
  compose.includes("volumes:\n  gateway-state:")
) {
  fail("the credential evidence gateway must use exact run-owned same-path bind state");
}
if (
  compose.includes(":latest") ||
  gatewayConfig.includes(":latest") ||
  gatewayConfig.includes("enable_bind_mounts = true")
) {
  fail("mutable images or unsafe sandbox bind mounts are present in the local topology");
}
const expectedRemainingBlockers = [
  "Capture, verify, and incorporate complete Docker-hosted OpenShell and Codex runtime conformance evidence for the pinned development profile.",
  "Publish and certify a tagged Rosetta v1 build, authenticated transport profile, stable error taxonomy, compatibility policy, and signed conformance fixtures before production materialization.",
];
if (
  profile.providerProfileEvidence?.codex?.status !==
    "credential non-exposure certified for the pinned development profile" ||
  profile.capabilities?.credentialNonExposure !== "certified for pinned development profile" ||
  profile.capabilities?.realSandboxInvocation !==
    "v2 evidence producer, immutable execution creator, private credential source, runtime provider binding, live case backend, scenario driver, OpenShell provider probes, and Codex runtime probes defined; Docker launcher and live certification blocked" ||
  JSON.stringify(profile.blockers) !== JSON.stringify(expectedRemainingBlockers)
) {
  fail("the accepted credential evidence claim or remaining blockers are inconsistent");
}

const expectedRuntimeChecks = [
  "gateway-ready",
  "sandbox-ready",
  "initialize",
  "turn-success",
  "turn-failure",
  "event-normalization",
  "interrupt",
  "cancellation",
  "command-approval",
  "file-change-approval",
  "artifact-export",
  "sandbox-teardown",
];
const expectedRuntimeClassifications = {
  text: "supported",
  "item-activity": "supported",
  interrupt: "supported",
  cancellation: "supported",
  failure: "supported",
  "command-approval": "supported",
  "file-change-approval": "supported",
  question: "unsupported",
  "permission-escalation": "unsupported",
  "rich-item-delta": "unsupported",
  usage: "unsupported",
  resume: "unsupported",
  steer: "unsupported",
  "artifact-export": "supported",
  "runtime-artifact-events": "unsupported",
};
const expectedRuntimeReasonCodes = {
  question: "ADAPTER_UNSUPPORTED",
  "permission-escalation": "ADAPTER_UNSUPPORTED",
  "rich-item-delta": "ADAPTER_UNSUPPORTED",
  usage: "ADAPTER_UNSUPPORTED",
  resume: "DURABLE_INTERACTION_UNIMPLEMENTED",
  steer: "DURABLE_INTERACTION_UNIMPLEMENTED",
  "runtime-artifact-events": "NATIVE_PROTOCOL_UNCERTIFIED",
};
const expectedRuntimeResourceNames = {
  gateway: "dg-runtime-gateway-<runID>",
  sandbox: "dg-runtime-sandbox-<runID>",
  provider: "dg-runtime-provider-<runID>",
  runtime: "dg-runtime-session-<runID>",
  workspace: "dg-runtime-<runID>",
};
const expectedRuntimeProvenance = {
  workflow: ".github/workflows/openshell-runtime-conformance.yml",
  artifactName: "openshell-runtime-conformance",
};
const runtimeConformance = profile.runtime?.conformance;
const runtimeEvidenceSchema = JSON.parse(
  await readFile(resolve(root, runtimeConformance?.schema ?? ""), "utf8"),
);
const runtimeProvenanceSchema = runtimeEvidenceSchema?.properties?.run?.properties?.provenance;
const runtimeProducer = runtimeConformance?.producer;
const runtimeEvidenceSource = await readFile(
  resolve(root, "internal/execution/runtimeevidence/evidence.go"),
  "utf8",
);
const runtimeEvidenceProfileSource = await readFile(
  resolve(root, "internal/execution/runtimeevidence/profile.go"),
  "utf8",
);
const runtimeLiveCaseSource = await readFile(
  resolve(root, "internal/execution/runtimeevidence/live.go"),
  "utf8",
);
const runtimeScenarioSource = await readFile(
  resolve(root, "internal/execution/runtimeevidence/scenario.go"),
  "utf8",
);
const runtimeOpenShellProbeSource = await readFile(
  resolve(root, "internal/execution/runtimeevidence/provider_probes.go"),
  "utf8",
);
const runtimeCodexProbeSource = await readFile(
  resolve(root, "internal/execution/runtimeevidence/runtime_probes.go"),
  "utf8",
);
const runtimeHarnessSource = await readFile(
  resolve(root, "internal/execution/runtimeevidence/harness.go"),
  "utf8",
);
const runtimeExecutionCreatorSource = await readFile(
  resolve(root, "internal/execution/runtimeevidence/execution_creator.go"),
  "utf8",
);
const runtimeCredentialSourceSource = await readFile(
  resolve(root, "internal/execution/runtimeevidence/credential_source.go"),
  "utf8",
);
const runtimeCredentialSourceUnixSource = await readFile(
  resolve(root, "internal/execution/runtimeevidence/credential_source_unix.go"),
  "utf8",
);
const runtimeProviderSource = await readFile(
  resolve(root, "internal/execution/runtimeevidence/provider_binding.go"),
  "utf8",
);
const runtimeProviderContractSource = await readFile(
  resolve(root, "internal/execution/provider_bindings.go"),
  "utf8",
);
const runtimeProviderOpenShellSource = await readFile(
  resolve(root, "internal/execution/openshell/provider_binding_runtime.go"),
  "utf8",
);
const runtimeScenarioDriver = runtimeProducer?.scenarioDriver;
const runtimeOpenShellProviderProbes = runtimeProducer?.openShellProviderProbes;
const runtimeCodexRuntimeProbes = runtimeProducer?.codexRuntimeProbes;
const runtimeHarness = runtimeProducer?.harness;
const runtimeExecutionCreator = runtimeProducer?.executionCreator;
const runtimeCredentialSource = runtimeProducer?.runtimeCredentialSource;
const runtimeProvider = runtimeProducer?.runtimeProvider;
const runtimeLiveCaseBackend = runtimeProducer?.liveCaseBackend;
if (
  runtimeConformance?.schema !== "deploy/openshell/runtime-conformance-evidence.schema.json" ||
  runtimeConformance?.schemaVersion !==
    "dataground.dev.openshell-runtime-conformance-evidence/v2" ||
  runtimeConformance?.verifier !== "pnpm openshell:runtime-evidence:check <evidence.json>" ||
  runtimeConformance?.verifierIdentity?.name !== "dataground-openshell-runtime-conformance" ||
  runtimeConformance?.verifierIdentity?.version !== "2.0.0" ||
  JSON.stringify(runtimeConformance?.requiredChecks) !== JSON.stringify(expectedRuntimeChecks) ||
  JSON.stringify(runtimeConformance?.capabilityClassifications) !==
    JSON.stringify(expectedRuntimeClassifications) ||
  JSON.stringify(runtimeConformance?.resourceNames) !==
    JSON.stringify(expectedRuntimeResourceNames) ||
  JSON.stringify(runtimeConformance?.provenance) !== JSON.stringify(expectedRuntimeProvenance) ||
  runtimeConformance?.status !==
    "v2 evidence producer, immutable execution creation, private credential acquisition, concrete probes, and closed composition harness defined; Docker launcher and live record required" ||
  runtimeEvidenceSchema?.properties?.schemaVersion?.const !== runtimeConformance.schemaVersion ||
  runtimeEvidenceSchema?.properties?.checks?.minItems !== expectedRuntimeChecks.length ||
  runtimeEvidenceSchema?.properties?.checks?.maxItems !== expectedRuntimeChecks.length ||
  runtimeEvidenceSchema?.properties?.capabilities?.minItems !==
    Object.keys(expectedRuntimeClassifications).length ||
  runtimeEvidenceSchema?.properties?.capabilities?.maxItems !==
    Object.keys(expectedRuntimeClassifications).length ||
  !runtimeEvidenceSchema?.properties?.profile?.required?.includes("credentialEvidenceSHA256") ||
  !runtimeEvidenceSchema?.properties?.run?.required?.includes("provenance") ||
  JSON.stringify(runtimeProvenanceSchema?.required) !==
    JSON.stringify(["sourceCommit", "workflow", "workflowRunID", "artifactName"]) ||
  runtimeProvenanceSchema?.properties?.workflow?.const !== expectedRuntimeProvenance.workflow ||
  runtimeProvenanceSchema?.properties?.artifactName?.const !==
    expectedRuntimeProvenance.artifactName ||
  runtimeEvidenceSchema?.properties?.run?.properties?.verifier?.properties?.name?.const !==
    runtimeConformance.verifierIdentity.name ||
  runtimeEvidenceSchema?.properties?.run?.properties?.verifier?.properties?.version?.const !==
    runtimeConformance.verifierIdentity.version ||
  Object.hasOwn(runtimeProvenanceSchema?.properties ?? {}, "artifactID") ||
  Object.hasOwn(runtimeProvenanceSchema?.properties ?? {}, "artifactArchiveDigest") ||
  runtimeEvidenceSchema?.properties?.result?.const !== "passed"
) {
  fail("the runtime conformance evidence contract is missing or inconsistent");
}
const runtimeProducerProfileBindings = [
  runtimeConformance.schemaVersion,
  profile.source.openshell.commit,
  profile.artifacts.gateway,
  profile.artifacts.supervisor,
  profile.artifacts.sandbox,
  profile.runtime.version,
  profile.runtime.schemaEvidence.canonicalSHA256,
  profile.providerProfileEvidence.contract.acceptedEvidence.sha256,
  profile.topology.gatewayEndpoint,
  profile.topology.driver,
  profile.topology.composeSHA256,
  profile.topology.gatewayConfigSHA256,
  runtimeConformance.verifierIdentity.name,
  runtimeConformance.verifierIdentity.version,
  runtimeConformance.provenance.workflow,
  runtimeConformance.provenance.artifactName,
];
if (
  runtimeProducer?.assembly !== "internal/execution/runtimeevidence.New" ||
  runtimeProducer?.execution !== "(*runtimeevidence.EvidenceRun).Execute" ||
  runtimeProducer?.binding !==
    "checked profile and verifier, source commit and workflow-run inputs, producer-owned workflow and artifact identity, run-derived resources, canonical case order, capabilities, cleanup, and result" ||
  runtimeProducer?.caseRunner !==
    "internal/execution/runtimeevidence.NewLiveCases with runtimeevidence.NewConcreteScenario" ||
  runtimeLiveCaseBackend?.assembly !== "internal/execution/runtimeevidence.NewLiveCases" ||
  runtimeLiveCaseBackend?.binding !==
    "exact run-derived portable gateway, sandbox, provider, runtime, and workspace identities" ||
  runtimeLiveCaseBackend?.dispatch !== "twelve typed scenario methods in canonical profile order" ||
  runtimeLiveCaseBackend?.observationProof !==
    "fixed 32-byte SHA-256 supplied by the exact live scenario; raw observations are not retained" ||
  runtimeLiveCaseBackend?.observationCommitment !==
    "four-byte length-prefixed SHA-256 over the v1 domain, run, case, resources, UTC interval, and observation proof" ||
  runtimeLiveCaseBackend?.observationCommitmentDomain !==
    "dataground.openshell-runtime-live-observation/v1" ||
  runtimeLiveCaseBackend?.exposure !==
    "each receipt must attest completed inspection; native-protocol or upstream-endpoint exposure fails before commitment" ||
  runtimeLiveCaseBackend?.concurrency !==
    "one in-flight case; overlap, order drift, cancellation, backend failure, invalid receipts, and proof replay are terminal" ||
  runtimeLiveCaseBackend?.completion !==
    "atomic backend finalization after the twelfth case and before cleanup" ||
  runtimeLiveCaseBackend?.serialization !== "forbidden" ||
  runtimeLiveCaseBackend?.status !==
    "implemented with scenario driver, concrete probes, and closed composition harness; Docker launcher required" ||
  runtimeScenarioDriver?.assembly !== "internal/execution/runtimeevidence.NewConcreteScenario" ||
  runtimeScenarioDriver?.binding !==
    "exact run-derived portable resources with separate provider lifecycle and Codex runtime probe ports" ||
  runtimeScenarioDriver?.dispatch !==
    "four provider lifecycle probes and eight runtime protocol probes in canonical evidence order" ||
  runtimeScenarioDriver?.assertions !==
    "one closed assertion shape per case; assertion substitution and extra claims fail closed" ||
  runtimeScenarioDriver?.exposure !==
    "each probe must attest inspection without native-protocol or upstream-endpoint exposure" ||
  runtimeScenarioDriver?.concurrency !==
    "single-use ordered state; overlap, cancellation, probe failure, binding drift, invalid assertions, and proof replay are terminal" ||
  runtimeScenarioDriver?.errors !== "native probe errors are replaced by stable scenario errors" ||
  runtimeScenarioDriver?.serialization !== "forbidden" ||
  runtimeScenarioDriver?.status !==
    "implemented with concrete probes and closed composition harness; Docker launcher required" ||
  runtimeOpenShellProviderProbes?.assembly !==
    "internal/execution/runtimeevidence.NewOpenShellProbes" ||
  runtimeOpenShellProviderProbes?.binding !==
    "run-derived durable isolation and operation identities, portable gateway, exact persisted execution, pinned loopback endpoint, Docker driver, and codex.app-server/v1 capability" ||
  JSON.stringify(runtimeOpenShellProviderProbes?.cases) !==
    JSON.stringify(["gateway-ready", "sandbox-ready", "artifact-export", "sandbox-teardown"]) ||
  runtimeOpenShellProviderProbes?.observationProof !==
    "domain-separated SHA-256 over the portable binding, UTC interval, private native routing identities, readiness state, and artifact digest" ||
  runtimeOpenShellProviderProbes?.artifact !==
    "exact run-derived sandbox path and content; exported bytes cleared after comparison" ||
  runtimeOpenShellProviderProbes?.failure !==
    "order drift, overlap, cancellation, native failure, binding mismatch, clock regression, artifact substitution, and uncertain teardown are terminal" ||
  runtimeOpenShellProviderProbes?.creation !==
    "pinned image, deny-all policy, run-derived provider profile, placement, and readiness are owned by the immutable execution creator" ||
  runtimeOpenShellProviderProbes?.serialization !== "configuration and adapter forbidden" ||
  runtimeOpenShellProviderProbes?.status !==
    "implemented with immutable execution creation, Codex runtime probes, and closed composition harness; Docker launcher required" ||
  runtimeCodexRuntimeProbes?.assembly !== "internal/execution/runtimeevidence.NewCodexProbes" ||
  runtimeCodexRuntimeProbes?.binding !==
    "exact run-derived portable resources, exact persisted ready execution, fresh OpenShell runtime session per case, and pinned internal Codex client" ||
  JSON.stringify(runtimeCodexRuntimeProbes?.cases) !==
    JSON.stringify([
      "initialize",
      "turn-success",
      "turn-failure",
      "event-normalization",
      "interrupt",
      "cancellation",
      "command-approval",
      "file-change-approval",
    ]) ||
  runtimeCodexRuntimeProbes?.requests !==
    "fixed run-bound prompts with locked read-only defaults; approval cases alone enable interactive review and file-change alone enables workspace-write under /tmp" ||
  runtimeCodexRuntimeProbes?.observationProof !==
    "domain-separated SHA-256 over the portable binding, UTC interval, bounded normalized event transcript, and closed case outcome" ||
  runtimeCodexRuntimeProbes?.acceptance !==
    "exact success text, native terminal failure, process event lifecycle, interrupt cancellation, canceled wait, and denied typed approvals; start and transport failures are rejected" ||
  runtimeCodexRuntimeProbes?.limits !==
    "at most 256 normalized events and 4194304 canonical JSON bytes per case" ||
  runtimeCodexRuntimeProbes?.failure !==
    "order drift, overlap, caller cancellation, execution substitution, native failure, uncertain session close, invalid event sequence, endpoint or native-field exposure, clock regression, and outcome mismatch are terminal" ||
  runtimeCodexRuntimeProbes?.serialization !== "configuration and adapter forbidden" ||
  runtimeCodexRuntimeProbes?.status !==
    "implemented with immutable execution creation and closed composition harness; Docker launcher required" ||
  runtimeHarness?.assembly !== "internal/execution/runtimeevidence.NewHarness" ||
  runtimeHarness?.binding !==
    "one run, workflow provenance, exact persisted execution, shared store and provider ports, and exact sandbox, provider-binding, and workspace cleanup functions" ||
  runtimeHarness?.composition !==
    "constructs the OpenShell and Codex probe adapters, concrete scenario, live cases, and evidence run internally" ||
  runtimeHarness?.chronology !==
    "public construction owns time.Now and shares it across every evidence layer" ||
  runtimeHarness?.singleUse !== "shared irreversible evidence state across harness copies" ||
  runtimeHarness?.failure !==
    "invalid or typed-nil ports fail before execution; runtime failures are sanitized and incomplete results remain non-serializable" ||
  runtimeHarness?.serialization !== "configuration and harness forbidden" ||
  runtimeHarness?.status !==
    "implemented with immutable execution creation; Docker topology and live record required" ||
  runtimeExecutionCreator?.assembly !== "internal/execution/runtimeevidence.NewExecutionCreator" ||
  runtimeExecutionCreator?.binding !==
    "one run-derived isolation domain, gateway, operation, sandbox, provider profile, and deterministic execution identity" ||
  runtimeExecutionCreator?.inputs !==
    "exact loopback Docker gateway, codex.app-server/v1 capability, digest-pinned sandbox image, and checked deny-all policy bytes and digest" ||
  runtimeExecutionCreator?.lifecycle !==
    "register and re-read the gateway, enable the exact provider profile, reserve placement, create once, and require a fresh exact ready observation" ||
  runtimeExecutionCreator?.recovery !==
    "observe deterministic persisted state before cancellation-independent termination; require a fresh exact terminal observation" ||
  runtimeExecutionCreator?.concurrency !==
    "shared irreversible create state across copies; overlap and replay poison creation without revoking cleanup authority" ||
  runtimeExecutionCreator?.serialization !== "configuration and creator forbidden" ||
  runtimeExecutionCreator?.status !==
    "implemented with runtime provider provisioning; Docker topology, launcher composition, workflow secret materialization, and live record required" ||
  runtimeCredentialSource?.assembly !==
    "internal/execution/runtimeevidence.NewRuntimeCredentialSource with NewRuntimeProviderFromCredentialSource" ||
  runtimeCredentialSource?.binding !==
    "one fresh absolute credential-bundle directory beneath an owner-controlled parent, containing only the four fixed Codex credential files" ||
  runtimeCredentialSource?.filesystem !==
    "non-symlink owner-held parent without group or world write, mode-0700 bundle, and single-link owner-held mode-0600 regular files of 1..65536 bytes held by exact filesystem identity" ||
  runtimeCredentialSource?.lifecycle !==
    "validate before acquisition, load once in fixed order, remove the exact files and empty bundle, synchronize the parent, then hand bytes directly to the runtime provisioner" ||
  runtimeCredentialSource?.failure !==
    "replay, overlap, cancellation, path substitution, extra entries, unsafe ownership or permissions, and uncertain removal fail closed without revoking exact cleanup authority" ||
  runtimeCredentialSource?.exposure !==
    "public callers receive only the runtime provisioner; credential bytes do not enter arguments, inherited environments, generic readers, serialization, or caller-visible return values" ||
  runtimeCredentialSource?.serialization !==
    "source configuration, provider-source configuration, source, credentials, and provisioner forbidden" ||
  runtimeCredentialSource?.status !==
    "implemented; Docker topology, launcher composition, workflow secret materialization, and live record required" ||
  runtimeProvider?.assembly !== "internal/execution/runtimeevidence.NewRuntimeProvider" ||
  runtimeProvider?.binding !==
    "one run-derived Codex provider name under the exact runtime isolation domain and gateway, with immutable native ID and resource version retained only for cleanup" ||
  runtimeProvider?.credentials !==
    "exact access_token, refresh_token, account_id, and id_token fields; each 1..65536 bytes, passed only through an allowlisted empty child environment and cleared after the first attempt" ||
  runtimeProvider?.lifecycle !==
    "require fresh absence before one create attempt, then accept only fresh exact codex metadata with the four checked credential keys" ||
  runtimeProvider?.recovery !==
    "lost create acknowledgement accepted only after exact credential-safe observation; cleanup requires exact identity and fresh timestamped absence" ||
  runtimeProvider?.concurrency !==
    "shared irreversible provisioning state across copies; overlap and replay poison provisioning without revoking cleanup authority" ||
  runtimeProvider?.serialization !==
    "credentials, requests, configuration, and provisioner forbidden" ||
  runtimeProvider?.status !==
    "implemented with private credential acquisition; Docker topology, launcher composition, workflow secret materialization, and live record required" ||
  JSON.stringify(runtimeProducer?.cleanupOrder) !==
    JSON.stringify(["sandbox", "providerBinding", "workspace"]) ||
  runtimeProducer?.serialization !==
    "run forbidden; result only after every case and cleanup succeeds" ||
  runtimeProducer?.status !==
    "closed assembler, immutable execution creation, concrete probes, and composition harness implemented; Docker launcher required" ||
  runtimeProducerProfileBindings.some(
    (binding) => !runtimeEvidenceProfileSource.includes(JSON.stringify(binding)),
  ) ||
  !runtimeEvidenceSource.includes("func New(") ||
  !runtimeEvidenceSource.includes("func (run *EvidenceRun) Execute(") ||
  !runtimeEvidenceSource.includes("func (EvidenceRun) MarshalJSON(") ||
  !runtimeEvidenceSource.includes("func (result Result) MarshalJSON(") ||
  !runtimeEvidenceSource.includes("context.WithoutCancel(ctx)") ||
  !runtimeEvidenceSource.includes("ErrAlreadyStarted") ||
  !runtimeEvidenceSource.includes("ErrRunIncomplete") ||
  !runtimeEvidenceSource.includes("namesForRun(state.config.RunID)") ||
  !runtimeEvidenceSource.includes("Capabilities: capabilities()") ||
  !runtimeEvidenceSource.includes("previousFinishedAt := startedAt") ||
  !runtimeEvidenceSource.includes("FinalizeBinding(config.RunID, resources)") ||
  !runtimeLiveCaseSource.includes("func NewLiveCases(") ||
  !runtimeLiveCaseSource.includes("type LiveScenario interface") ||
  !runtimeLiveCaseSource.includes("func (cases *LiveCases) FinalizeBinding(") ||
  !runtimeLiveCaseSource.includes("liveObservationCommitmentDomain") ||
  !runtimeLiveCaseSource.includes("writeLiveCommitmentPart") ||
  !runtimeLiveCaseSource.includes("ExposureChecked") ||
  !runtimeLiveCaseSource.includes("!receipt.NativeProtocolExposed") ||
  !runtimeLiveCaseSource.includes("!receipt.UpstreamEndpointExposed") ||
  !runtimeLiveCaseSource.includes("ErrLiveCaseReplay") ||
  !runtimeScenarioSource.includes("func NewConcreteScenario(") ||
  !runtimeScenarioSource.includes("type ProviderProbes interface") ||
  !runtimeScenarioSource.includes("type RuntimeProbes interface") ||
  !runtimeScenarioSource.includes("func validProbeResult(") ||
  !runtimeScenarioSource.includes("ErrScenarioReplay") ||
  !runtimeScenarioSource.includes("func (ConcreteScenario) MarshalJSON(") ||
  !runtimeOpenShellProbeSource.includes("func NewOpenShellProbes(") ||
  !runtimeOpenShellProbeSource.includes('identity.Derived("iso", "runtime-conformance:"') ||
  !runtimeOpenShellProbeSource.includes('identity.Derived("op", "runtime-conformance:"') ||
  !runtimeOpenShellProbeSource.includes("state.store.GetGateway(") ||
  !runtimeOpenShellProbeSource.includes("state.provider.Observe(") ||
  !runtimeOpenShellProbeSource.includes("state.provider.Export(") ||
  !runtimeOpenShellProbeSource.includes("clear(result.Content)") ||
  !runtimeOpenShellProbeSource.includes("state.provider.Terminate(") ||
  !runtimeOpenShellProbeSource.includes("openShellProbeCommitmentDomain") ||
  !runtimeOpenShellProbeSource.includes("func (OpenShellProbeConfig) MarshalJSON(") ||
  !runtimeOpenShellProbeSource.includes("func (OpenShellProbes) MarshalJSON(") ||
  !runtimeCodexProbeSource.includes("func NewCodexProbes(") ||
  !runtimeCodexProbeSource.includes("state.provider.StartRuntime(") ||
  !runtimeCodexProbeSource.includes("codex.New(trackedSession)") ||
  !runtimeCodexProbeSource.includes("dgruntime.ApprovalLocked") ||
  !runtimeCodexProbeSource.includes("dgruntime.ApprovalInteractive") ||
  !runtimeCodexProbeSource.includes("dgruntime.SandboxWorkspaceWrite") ||
  !runtimeCodexProbeSource.includes("maxCodexProbeEvents") ||
  !runtimeCodexProbeSource.includes("maxCodexProbeEventBytes") ||
  !runtimeCodexProbeSource.includes("valueExposesNativeProtocol") ||
  !runtimeCodexProbeSource.includes("trackedCodexProbeSession") ||
  !runtimeCodexProbeSource.includes("codexProbeCommitmentDomain") ||
  !runtimeCodexProbeSource.includes("func (CodexProbeConfig) MarshalJSON(") ||
  !runtimeCodexProbeSource.includes("func (CodexProbes) MarshalJSON(") ||
  !runtimeExecutionCreatorSource.includes("func NewExecutionCreator(") ||
  !runtimeExecutionCreatorSource.includes("execution.VerifyEnforcementPolicy(") ||
  !runtimeExecutionCreatorSource.includes("state.provider.RegisterGateway(") ||
  !runtimeExecutionCreatorSource.includes("state.store.GetGateway(") ||
  !runtimeExecutionCreatorSource.includes("state.provider.EnableProviderProfiles(") ||
  !runtimeExecutionCreatorSource.includes("state.provider.SelectGateway(") ||
  !runtimeExecutionCreatorSource.includes("state.provider.Create(") ||
  !runtimeExecutionCreatorSource.includes("state.waitForReady(") ||
  !runtimeExecutionCreatorSource.includes("context.WithoutCancel(ctx)") ||
  !runtimeExecutionCreatorSource.includes("state.cleanupPersisted(") ||
  !runtimeExecutionCreatorSource.includes("func (ExecutionCreationConfig) MarshalJSON(") ||
  !runtimeExecutionCreatorSource.includes("func (ExecutionCreator) MarshalJSON(") ||
  runtimeExecutionCreatorSource.includes("os/exec") ||
  runtimeExecutionCreatorSource.includes('"docker"') ||
  runtimeExecutionCreatorSource.includes('"openshell"') ||
  !runtimeCredentialSourceSource.includes("func NewRuntimeCredentialSource(") ||
  !runtimeCredentialSourceSource.includes("func NewRuntimeProviderFromCredentialSource(") ||
  !runtimeCredentialSourceSource.includes("func (source *RuntimeCredentialSource) load(") ||
  !runtimeCredentialSourceSource.includes("func (source *RuntimeCredentialSource) Cleanup(") ||
  !runtimeCredentialSourceSource.includes("exactRuntimeCredentialEntries") ||
  !runtimeCredentialSourceSource.includes("safeRuntimeCredentialParent") ||
  !runtimeCredentialSourceSource.includes("safeRuntimeCredentialDirectory") ||
  !runtimeCredentialSourceSource.includes("safeRuntimeCredentialFile") ||
  !runtimeCredentialSourceSource.includes("os.SameFile(") ||
  !runtimeCredentialSourceSource.includes("os.Remove(owned.path)") ||
  !runtimeCredentialSourceSource.includes("state.parent.Sync()") ||
  !runtimeCredentialSourceSource.includes("clearRuntimeProviderCredentials(&credentials)") ||
  !runtimeCredentialSourceSource.includes("func (CredentialSourceConfig) MarshalJSON(") ||
  !runtimeCredentialSourceSource.includes("func (RuntimeProviderSourceConfig) MarshalJSON(") ||
  !runtimeCredentialSourceSource.includes("func (RuntimeCredentialSource) MarshalJSON(") ||
  !runtimeCredentialSourceUnixSource.includes("stat.Uid == uint32(os.Geteuid())") ||
  !runtimeCredentialSourceUnixSource.includes("stat.Nlink == 1") ||
  runtimeCredentialSourceSource.includes("os.LookupEnv(") ||
  runtimeCredentialSourceSource.includes("os.Getenv(") ||
  runtimeCredentialSourceSource.includes("os.Environ(") ||
  runtimeCredentialSourceSource.includes("os/exec") ||
  runtimeCredentialSourceSource.includes('"docker"') ||
  runtimeCredentialSourceSource.includes('"openshell"') ||
  !runtimeProviderContractSource.includes("type RuntimeConformanceCredentials struct") ||
  !runtimeProviderContractSource.includes("type RuntimeConformanceProviderProvisioner interface") ||
  !runtimeProviderContractSource.includes("func (RuntimeConformanceCredentials) MarshalJSON(") ||
  !runtimeProviderContractSource.includes(
    "func (RuntimeConformanceProviderRequest) MarshalJSON(",
  ) ||
  !runtimeProviderOpenShellSource.includes(
    "func (provider *Provider) CreateRuntimeConformanceProvider(",
  ) ||
  !runtimeProviderOpenShellSource.includes(
    "func (provider *Provider) ObserveRuntimeConformanceProvider(",
  ) ||
  !runtimeProviderOpenShellSource.includes("runtimeConformanceProviderKeys") ||
  !runtimeProviderOpenShellSource.includes("RunWithCredentials(") ||
  !runtimeProviderOpenShellSource.includes("clearRuntimeConformanceCredentialMap") ||
  runtimeProviderOpenShellSource.includes("os.Environ()") ||
  runtimeProviderOpenShellSource.includes('"sh", "-c"') ||
  !providerBindingCreateSource.includes("command.Env = make(") ||
  !runtimeProviderSource.includes("func NewRuntimeProvider(") ||
  !runtimeProviderSource.includes("func (provider *RuntimeProvider) Provision(") ||
  !runtimeProviderSource.includes("func (provider *RuntimeProvider) Cleanup(") ||
  !runtimeProviderSource.includes("ObserveRuntimeConformanceProvider(") ||
  !runtimeProviderSource.includes("CreateRuntimeConformanceProvider(") ||
  !runtimeProviderSource.includes("clearRuntimeProviderCredentials") ||
  !runtimeProviderSource.includes("context.WithoutCancel(ctx)") ||
  !runtimeProviderSource.includes("func (RuntimeProviderConfig) MarshalJSON(") ||
  !runtimeProviderSource.includes("func (RuntimeProvider) MarshalJSON(") ||
  runtimeProviderSource.includes("os/exec") ||
  runtimeProviderSource.includes('"docker"') ||
  runtimeProviderSource.includes('"openshell"') ||
  !runtimeHarnessSource.includes("func NewHarness(") ||
  !runtimeHarnessSource.includes("return newHarness(config, time.Now)") ||
  !runtimeHarnessSource.includes("NewOpenShellProbes(") ||
  !runtimeHarnessSource.includes("NewCodexProbes(") ||
  !runtimeHarnessSource.includes("NewConcreteScenario(") ||
  !runtimeHarnessSource.includes("NewLiveCases(") ||
  !runtimeHarnessSource.includes("newEvidenceRun(") ||
  !runtimeHarnessSource.includes("isNilHarnessPort") ||
  !runtimeHarnessSource.includes("func (HarnessConfig) MarshalJSON(") ||
  !runtimeHarnessSource.includes("func (Harness) MarshalJSON(") ||
  runtimeHarnessSource.includes("os/exec") ||
  runtimeHarnessSource.includes('"docker"') ||
  runtimeHarnessSource.includes('"openshell"') ||
  runtimeCodexProbeSource.includes("os/exec") ||
  runtimeOpenShellProbeSource.includes("os/exec") ||
  runtimeScenarioSource.includes("os/exec") ||
  runtimeScenarioSource.includes('"docker"') ||
  runtimeScenarioSource.includes('"openshell"') ||
  runtimeLiveCaseSource.includes("os/exec") ||
  runtimeLiveCaseSource.includes('"docker"') ||
  runtimeLiveCaseSource.includes('"openshell"') ||
  runtimeEvidenceSource.includes("os/exec") ||
  runtimeEvidenceSource.includes('"docker"') ||
  runtimeEvidenceSource.includes('"openshell"') ||
  !runtimeEvidenceProfileSource.includes("func currentProfile() profile") ||
  Object.entries(expectedRuntimeReasonCodes).some(
    ([name, reason]) =>
      !runtimeEvidenceSource.includes(`Name: "${name}"`) ||
      !runtimeEvidenceSource.includes(`unsupported("${reason}")`),
  )
) {
  fail(
    "the runtime evidence producer, immutable execution creator, private credential source, runtime provider binding, concrete probes, or closed composition harness are missing, mutable, or overclaim live certification",
  );
}

const credentialEvidenceWorkflow = await readFile(
  resolve(root, ".github/workflows/openshell-credential-evidence.yml"),
  "utf8",
);
const expectedCredentialEvidenceWorkflowBindings = [
  "runs-on: ubuntu-24.04",
  "permissions:\n  contents: read",
  "OPENSHELL_VERSION: v0.0.86",
  "OPENSHELL_PACKAGE: openshell_0.0.86-1_amd64.deb",
  "OPENSHELL_PACKAGE_SHA256: 3e757ca2f1e855d6e3bcc328fbd69e0441a9a6d01dfe14902f525185dd55ada4",
  "NVIDIA/OpenShell/releases/download/$OPENSHELL_VERSION",
  "sha256sum --check --strict -",
  'dpkg-deb --extract "$package" "$cli_root"',
  "OPENSHELL_CLI=%s",
  '--openshell-binary "$OPENSHELL_CLI"',
  "go run ./cmd/dataground-openshell-canary",
  "pnpm openshell:credential-evidence:check",
  "if: always()",
  "--filter label=com.docker.compose.project",
  '--filter "ancestor=$image"',
  ".dataground-policy-workspace.lock",
  "actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02",
  "if-no-files-found: error",
  "if: success()",
];
const cleanupStep = credentialEvidenceWorkflow.indexOf("- name: Require complete resource removal");
const uploadStep = credentialEvidenceWorkflow.indexOf("- name: Upload verified evidence");
if (
  expectedCredentialEvidenceWorkflowBindings.some(
    (binding) => !credentialEvidenceWorkflow.includes(binding),
  ) ||
  credentialEvidenceWorkflow.includes("pull_request_target:") ||
  credentialEvidenceWorkflow.includes("contents: write") ||
  credentialEvidenceWorkflow.includes("secrets:") ||
  credentialEvidenceWorkflow.includes("install.sh") ||
  credentialEvidenceWorkflow.includes("apt-get") ||
  credentialEvidenceWorkflow.includes("systemctl") ||
  credentialEvidenceWorkflow.includes("gateway add") ||
  cleanupStep < 0 ||
  uploadStep < 0 ||
  cleanupStep > uploadStep
) {
  fail("the live credential evidence workflow is missing or unsafe");
}

if (failures.length > 0) {
  console.error(failures.map((message) => `- ${message}`).join("\n"));
  process.exitCode = 1;
} else {
  console.log("OpenShell development profile is internally consistent.");
}
