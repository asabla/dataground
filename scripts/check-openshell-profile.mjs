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
    "dataground.dev.openshell-credential-non-exposure-evidence/v2" ||
  credentialEvidenceContract?.verifier !==
    "pnpm openshell:credential-evidence:check <evidence.json>" ||
  JSON.stringify(Object.keys(credentialEvidenceContract?.verifierIdentity ?? {}).sort()) !==
    JSON.stringify(["name", "version"]) ||
  credentialEvidenceContract?.verifierIdentity?.name !== "dataground-openshell-canary" ||
  credentialEvidenceContract?.verifierIdentity?.version !== "2.0.0" ||
  credentialEvidenceContract?.status !== "contract and launcher verified; live evidence required"
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
  canaryScanner?.status !== "commitment-only scanner verified; live Docker execution required"
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
    "collection, acquisition, and launcher wiring verified; live Docker execution required" ||
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
    "all seven source backends and launcher wiring verified; live Docker execution required" ||
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
    "OpenShell sandbox source backend and launcher wiring verified; live Docker execution required" ||
  canarySourceAcquisition?.docker?.assembly !== "internal/security/canarydocker.New" ||
  canarySourceAcquisition?.docker?.binding !==
    "exact running gateway container ID, digest-pinned image, Compose project and service, and run, gateway, and provider evidence labels" ||
  canarySourceAcquisition?.docker?.transport !==
    "streaming Docker inspect argument vectors and complete timestamped stdout and stderr logs without a shell" ||
  JSON.stringify(canarySourceAcquisition?.docker?.surfaces) !==
    JSON.stringify(["provider-arguments", "gateway-logs"]) ||
  canarySourceAcquisition?.docker?.serialization !== "forbidden" ||
  canarySourceAcquisition?.docker?.status !==
    "Docker host source backend and launcher wiring verified; live Docker execution required" ||
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
    "runtime-error source backend and launcher wiring verified; live Docker execution required" ||
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
  !canaryOpenShellSource.includes("const rootDevice = fs.statSync(root).dev;") ||
  !canaryOpenShellSource.includes("const buffer = Buffer.alloc(64 * 1024);") ||
  !canaryOpenShellSource.includes("stat.dev === rootDevice") ||
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
    "checked profile snapshot, topology digests, and dataground-openshell-canary/2.0.0 verifier identity" ||
  JSON.stringify(canaryEvidenceRun?.cleanupOrder) !==
    JSON.stringify(["sandbox", "provider", "workspace"]) ||
  canaryEvidenceRun?.cleanupContext !== "cancellation-independent run-owned cleanup" ||
  canaryEvidenceRun?.serialization !== "sealed until collection and every cleanup succeeds" ||
  canaryEvidenceRun?.workspaceCleanup?.assembly !== "internal/security/canaryworkspace.Open" ||
  canaryEvidenceRun?.workspaceCleanup?.name !== "dg-canary-<runID>" ||
  canaryEvidenceRun?.workspaceCleanup?.parent !== "pre-existing owner-only mode-0700 directory" ||
  canaryEvidenceRun?.workspaceCleanup?.serialization !== "forbidden" ||
  canaryEvidenceRun?.workspaceCleanup?.status !==
    "workspace lifecycle and launcher wiring verified; live Docker execution required" ||
  canaryEvidenceRun?.sandboxCleanup?.assembly !== "internal/security/canarysandbox.New" ||
  canaryEvidenceRun?.sandboxCleanup?.resource !==
    "exact DataGround execution returned by the OpenShell provider" ||
  canaryEvidenceRun?.sandboxCleanup?.verification !==
    "terminate then require a timestamped exact terminal observation" ||
  canaryEvidenceRun?.sandboxCleanup?.serialization !== "forbidden" ||
  canaryEvidenceRun?.sandboxCleanup?.status !==
    "sandbox cleanup and launcher wiring verified; live Docker execution required" ||
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
    "provider provisioning, closed composition, and launcher wiring verified; live Docker execution required" ||
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
    "provider cleanup and launcher wiring verified; live Docker execution required" ||
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
    "closed run and launcher composition verified; live Docker execution required" ||
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
    "launcher implementation verified; live Docker execution required" ||
  canaryEvidenceRun?.status !==
    "provider provisioning, run, source lifecycle, cleanup, closed composition, and launcher verified; live Docker execution required" ||
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
if (!Array.isArray(profile.blockers) || profile.blockers.length === 0) {
  fail("incomplete live certification blockers must be explicit");
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
