import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

const root = resolve(import.meta.dirname, "..");
const profilePath = resolve(root, "deploy/openshell/development-profile.json");
const schemaPath = resolve(root, "deploy/openshell/runtime-conformance-evidence.schema.json");
const acceptanceSchemaPath = resolve(
  root,
  "deploy/openshell/runtime-conformance-acceptance.schema.json",
);
const requiredChecks = [
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
const expectedClassifications = {
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
const expectedEvidence = {
  text: ["turn-success", "event-normalization"],
  "item-activity": ["event-normalization"],
  interrupt: ["interrupt"],
  cancellation: ["cancellation"],
  failure: ["turn-failure"],
  "command-approval": ["command-approval"],
  "file-change-approval": ["file-change-approval"],
  "artifact-export": ["artifact-export"],
};
const expectedReasonCodes = {
  question: "ADAPTER_UNSUPPORTED",
  "permission-escalation": "ADAPTER_UNSUPPORTED",
  "rich-item-delta": "ADAPTER_UNSUPPORTED",
  usage: "ADAPTER_UNSUPPORTED",
  resume: "DURABLE_INTERACTION_UNIMPLEMENTED",
  steer: "DURABLE_INTERACTION_UNIMPLEMENTED",
  "runtime-artifact-events": "NATIVE_PROTOCOL_UNCERTIFIED",
};
const expectedResourceNames = {
  gateway: "dg-runtime-gateway-<runID>",
  sandbox: "dg-runtime-sandbox-<runID>",
  provider: "dg-runtime-provider-<runID>",
  runtime: "dg-runtime-session-<runID>",
  workspace: "dg-runtime-<runID>",
};
const expectedProvenance = {
  workflow: ".github/workflows/openshell-runtime-conformance.yml",
  artifactName: "openshell-runtime-conformance",
};

const [profile, schema, acceptanceSchema] = await Promise.all([
  readJSON(profilePath),
  readJSON(schemaPath),
  readJSON(acceptanceSchemaPath),
]);
const contract = profile.runtime?.conformance;
const ajv = new Ajv2020({ allErrors: true, strict: true });
addFormats(ajv);
const validateSchema = ajv.compile(schema);
const validateAcceptanceSchema = ajv.compile(acceptanceSchema);

const expectedProfile = {
  openshellCommit: profile.source.openshell.commit,
  gatewayImage: contract.topology.gatewayImage,
  supervisorImage: profile.artifacts.supervisor,
  sandboxImage: profile.artifacts.sandbox,
  runtimeVersion: profile.runtime.version,
  runtimeSchemaCanonicalSHA256: profile.runtime.schemaEvidence.canonicalSHA256,
  credentialEvidenceSHA256: profile.providerProfileEvidence.contract.acceptedEvidence.sha256,
  gatewayEndpoint: contract.topology.gatewayEndpoint,
  driver: contract.topology.driver,
  composeSHA256: contract.topology.composeSHA256,
  gatewayConfigSHA256: contract.topology.gatewayConfigSHA256,
};

function verifyEvidence(evidence) {
  const failures = [];
  if (!validateSchema(evidence)) {
    failures.push(
      ...validateSchema.errors.map((error) => `${error.instancePath || "/"} ${error.message}`),
    );
    return failures;
  }
  if (
    Object.entries(expectedProfile).some(
      ([field, expected]) => evidence.profile[field] !== expected,
    )
  ) {
    failures.push("evidence does not match the checked-in OpenShell runtime profile");
  }
  if (
    contract?.schemaVersion !== evidence.schemaVersion ||
    contract?.verifierIdentity?.name !== evidence.run.verifier.name ||
    contract?.verifierIdentity?.version !== evidence.run.verifier.version ||
    JSON.stringify(contract?.resourceNames) !== JSON.stringify(expectedResourceNames) ||
    JSON.stringify(contract?.provenance) !== JSON.stringify(expectedProvenance) ||
    evidence.run.provenance.workflow !== expectedProvenance.workflow ||
    evidence.run.provenance.artifactName !== expectedProvenance.artifactName
  ) {
    failures.push("evidence does not use the checked-in runtime verifier contract");
  }

  const checks = evidence.checks.map((check) => check.name);
  if (
    checks.length !== new Set(checks).size ||
    JSON.stringify(checks) !== JSON.stringify(requiredChecks) ||
    JSON.stringify([...(contract?.requiredChecks ?? [])]) !== JSON.stringify(requiredChecks)
  ) {
    failures.push("evidence must contain every checked runtime case exactly once");
  }
  const commitments = evidence.checks.map((check) => check.observationCommitment);
  if (commitments.length !== new Set(commitments).size) {
    failures.push("each runtime check must have a distinct observation commitment");
  }

  const capabilities = new Map(
    evidence.capabilities.map((capability) => [capability.name, capability]),
  );
  if (
    capabilities.size !== Object.keys(expectedClassifications).length ||
    Object.entries(expectedClassifications).some(
      ([name, classification]) => capabilities.get(name)?.classification !== classification,
    ) ||
    Object.entries(contract?.capabilityClassifications ?? {}).some(
      ([name, classification]) => expectedClassifications[name] !== classification,
    ) ||
    Object.keys(contract?.capabilityClassifications ?? {}).length !==
      Object.keys(expectedClassifications).length
  ) {
    failures.push("runtime capability classifications drift from the checked profile");
  }
  for (const [name, expectedChecks] of Object.entries(expectedEvidence)) {
    const actual = capabilities.get(name)?.evidence ?? [];
    if (JSON.stringify([...actual].sort()) !== JSON.stringify([...expectedChecks].sort())) {
      failures.push(`${name} is not bound to its required live checks`);
    }
  }
  for (const [name, expectedReasonCode] of Object.entries(expectedReasonCodes)) {
    if (capabilities.get(name)?.reasonCode !== expectedReasonCode) {
      failures.push(`${name} does not use its profile-owned limitation reason`);
    }
  }
  if (
    evidence.capabilities.some(
      (capability) =>
        capability.evidence.some((check) => !checks.includes(check)) ||
        (capability.classification === "supported" && capability.reasonCode !== null) ||
        (capability.classification !== "supported" &&
          (capability.evidence.length !== 0 || capability.reasonCode === null)),
    )
  ) {
    failures.push("runtime capability evidence or limitation classification is inconsistent");
  }

  const runStartedAt = Date.parse(evidence.run.startedAt);
  const runFinishedAt = Date.parse(evidence.run.finishedAt);
  if (
    !Number.isFinite(runStartedAt) ||
    !Number.isFinite(runFinishedAt) ||
    runFinishedAt < runStartedAt
  ) {
    failures.push("runtime evidence timestamps are invalid or out of order");
  }
  let previousFinishedAt = runStartedAt;
  for (const check of evidence.checks) {
    const startedAt = Date.parse(check.startedAt);
    const finishedAt = Date.parse(check.finishedAt);
    if (
      !Number.isFinite(startedAt) ||
      !Number.isFinite(finishedAt) ||
      finishedAt < startedAt ||
      startedAt < runStartedAt ||
      startedAt < previousFinishedAt ||
      finishedAt > runFinishedAt
    ) {
      failures.push(`${check.name} observation window is outside the evidence run`);
    }
    previousFinishedAt = finishedAt;
  }

  const names = Object.fromEntries(
    Object.entries(expectedResourceNames).map(([kind, template]) => [
      kind,
      template.replace("<runID>", evidence.run.id),
    ]),
  );
  if (
    Object.entries(names).some(([kind, expected]) => evidence.run.resources[kind] !== expected) ||
    evidence.cleanup.sandbox.name !== evidence.run.resources.sandbox ||
    evidence.cleanup.providerBinding.name !== evidence.run.resources.provider ||
    evidence.cleanup.workspace.name !== evidence.run.resources.workspace
  ) {
    failures.push("runtime resources and cleanup are not bound to the evidence run");
  }
  return failures;
}

function verifyAcceptance(evidenceBytes, evidence, evidenceFile, acceptance) {
  const failures = [];
  if (!validateAcceptanceSchema(acceptance)) {
    failures.push(
      ...validateAcceptanceSchema.errors.map(
        (error) => `acceptance${error.instancePath || "/"} ${error.message}`,
      ),
    );
    return failures;
  }
  const expectedDigest = createHash("sha256").update(evidenceBytes).digest("hex");
  const expectedFile = evidenceFile.replaceAll("\\", "/");
  if (
    acceptance.evidence.file !== expectedFile ||
    acceptance.evidence.sha256 !== expectedDigest ||
    acceptance.evidence.runID !== evidence.run.id
  ) {
    failures.push("acceptance does not bind the exact evidence record");
  }
  if (
    acceptance.workflow.path !== evidence.run.provenance.workflow ||
    acceptance.workflow.runID !== evidence.run.provenance.workflowRunID ||
    acceptance.workflow.headCommit !== evidence.run.provenance.sourceCommit
  ) {
    failures.push("acceptance does not bind the evidence workflow provenance");
  }
  if (
    acceptance.artifact.name !== evidence.run.provenance.artifactName ||
    acceptance.artifact.name !== expectedProvenance.artifactName
  ) {
    failures.push("acceptance does not bind the evidence artifact identity");
  }
  return failures;
}

function representativeEvidence() {
  const id = "0123456789abcdef0123456789abcdef";
  const checks = requiredChecks.map((name, index) => ({
    name,
    status: "passed",
    startedAt: `2026-07-30T12:00:${String(index * 2).padStart(2, "0")}.000Z`,
    finishedAt: `2026-07-30T12:00:${String(index * 2 + 1).padStart(2, "0")}.000Z`,
    observationCommitment: `sha256:${index.toString(16).padStart(64, "0")}`,
    nativeProtocolExposed: false,
    upstreamEndpointExposed: false,
  }));
  return {
    schemaVersion: "dataground.dev.openshell-runtime-conformance-evidence/v2",
    profile: structuredClone(expectedProfile),
    run: {
      id,
      resources: {
        gateway: `dg-runtime-gateway-${id}`,
        sandbox: `dg-runtime-sandbox-${id}`,
        provider: `dg-runtime-provider-${id}`,
        runtime: `dg-runtime-session-${id}`,
        workspace: `dg-runtime-${id}`,
      },
      startedAt: "2026-07-30T12:00:00.000Z",
      finishedAt: "2026-07-30T12:01:00.000Z",
      verifier: structuredClone(contract.verifierIdentity),
      provenance: {
        sourceCommit: "a".repeat(40),
        workflow: expectedProvenance.workflow,
        workflowRunID: 123,
        artifactName: expectedProvenance.artifactName,
      },
    },
    checks,
    capabilities: Object.entries(expectedClassifications).map(([name, classification]) => ({
      name,
      classification,
      evidence: structuredClone(expectedEvidence[name] ?? []),
      reasonCode: classification === "supported" ? null : expectedReasonCodes[name],
    })),
    cleanup: {
      sandbox: { name: `dg-runtime-sandbox-${id}`, status: "removed" },
      providerBinding: { name: `dg-runtime-provider-${id}`, status: "removed" },
      workspace: { name: `dg-runtime-${id}`, status: "removed" },
    },
    result: "passed",
  };
}

function runSelfTest() {
  const valid = representativeEvidence();
  const mutations = [
    ["representative evidence", valid, true],
    ["profile drift", { ...valid, profile: { ...valid.profile, runtimeVersion: "0.0.0" } }, false],
    [
      "credential evidence drift",
      {
        ...valid,
        profile: { ...valid.profile, credentialEvidenceSHA256: "c".repeat(64) },
      },
      false,
    ],
    ["missing provenance", { ...valid, run: { ...valid.run, provenance: undefined } }, false],
    [
      "wrong workflow provenance",
      {
        ...valid,
        run: {
          ...valid.run,
          provenance: {
            ...valid.run.provenance,
            workflow: ".github/workflows/other.yml",
          },
        },
      },
      false,
    ],
    [
      "producer-forged upload provenance",
      {
        ...valid,
        run: {
          ...valid.run,
          provenance: { ...valid.run.provenance, artifactID: 456 },
        },
      },
      false,
    ],
    [
      "missing workflow run provenance",
      {
        ...valid,
        run: {
          ...valid.run,
          provenance: { ...valid.run.provenance, workflowRunID: undefined },
        },
      },
      false,
    ],
    ["missing runtime case", { ...valid, checks: valid.checks.slice(1) }, false],
    [
      "reordered runtime cases",
      {
        ...valid,
        checks: valid.checks.map((check, index, checks) =>
          index === 0 ? checks[1] : index === 1 ? checks[0] : check,
        ),
      },
      false,
    ],
    [
      "duplicate runtime case",
      {
        ...valid,
        checks: valid.checks.map((check, index) =>
          index === 1 ? { ...check, name: valid.checks[0].name } : check,
        ),
      },
      false,
    ],
    [
      "reused observation",
      {
        ...valid,
        checks: valid.checks.map((check, index) =>
          index === 1
            ? {
                ...check,
                observationCommitment: valid.checks[0].observationCommitment,
              }
            : check,
        ),
      },
      false,
    ],
    [
      "capability overclaim",
      {
        ...valid,
        capabilities: valid.capabilities.map((capability) =>
          capability.name === "question"
            ? {
                ...capability,
                classification: "supported",
                evidence: ["turn-success"],
                reasonCode: null,
              }
            : capability,
        ),
      },
      false,
    ],
    [
      "duplicate capability",
      {
        ...valid,
        capabilities: valid.capabilities.map((capability, index) =>
          index === 1 ? { ...capability, name: valid.capabilities[0].name } : capability,
        ),
      },
      false,
    ],
    [
      "missing supported evidence",
      {
        ...valid,
        capabilities: valid.capabilities.map((capability) =>
          capability.name === "interrupt" ? { ...capability, evidence: [] } : capability,
        ),
      },
      false,
    ],
    [
      "wrong limitation reason",
      {
        ...valid,
        capabilities: valid.capabilities.map((capability) =>
          capability.name === "resume"
            ? { ...capability, reasonCode: "ADAPTER_UNSUPPORTED" }
            : capability,
        ),
      },
      false,
    ],
    [
      "unbound capability evidence",
      {
        ...valid,
        capabilities: valid.capabilities.map((capability) =>
          capability.name === "interrupt"
            ? { ...capability, evidence: ["turn-success"] }
            : capability,
        ),
      },
      false,
    ],
    [
      "native protocol exposure",
      {
        ...valid,
        checks: valid.checks.map((check, index) =>
          index === 0 ? { ...check, nativeProtocolExposed: true } : check,
        ),
      },
      false,
    ],
    [
      "upstream endpoint exposure",
      {
        ...valid,
        checks: valid.checks.map((check, index) =>
          index === 0 ? { ...check, upstreamEndpointExposed: true } : check,
        ),
      },
      false,
    ],
    [
      "reversed run window",
      {
        ...valid,
        run: {
          ...valid.run,
          startedAt: "2026-07-30T12:02:00.000Z",
          finishedAt: "2026-07-30T12:01:00.000Z",
        },
      },
      false,
    ],
    [
      "stale observation window",
      {
        ...valid,
        checks: valid.checks.map((check, index) =>
          index === 0 ? { ...check, startedAt: "2026-07-30T11:59:59.000Z" } : check,
        ),
      },
      false,
    ],
    [
      "overlapping observation window",
      {
        ...valid,
        checks: valid.checks.map((check, index) =>
          index === 1 ? { ...check, startedAt: valid.checks[0].startedAt } : check,
        ),
      },
      false,
    ],
    [
      "wrong cleanup target",
      {
        ...valid,
        cleanup: {
          ...valid.cleanup,
          sandbox: { ...valid.cleanup.sandbox, name: "other-sandbox" },
        },
      },
      false,
    ],
    [
      "uncertain cleanup",
      {
        ...valid,
        cleanup: {
          ...valid.cleanup,
          workspace: { ...valid.cleanup.workspace, status: "unknown" },
        },
      },
      false,
    ],
    [
      "unbound workspace",
      {
        ...valid,
        run: {
          ...valid.run,
          resources: {
            ...valid.run.resources,
            workspace: "dg-runtime-fedcba9876543210fedcba9876543210",
          },
        },
        cleanup: {
          ...valid.cleanup,
          workspace: {
            ...valid.cleanup.workspace,
            name: "dg-runtime-fedcba9876543210fedcba9876543210",
          },
        },
      },
      false,
    ],
    [
      "mixed runtime identity",
      {
        ...valid,
        run: {
          ...valid.run,
          resources: {
            ...valid.run.resources,
            runtime: "dg-runtime-session-fedcba9876543210fedcba9876543210",
          },
        },
      },
      false,
    ],
  ];
  const failures = mutations.flatMap(([name, evidence, shouldPass]) => {
    const passed = verifyEvidence(evidence).length === 0;
    return passed === shouldPass ? [] : [`self-test failed: ${name}`];
  });
  const evidenceBytes = Buffer.from(JSON.stringify(valid));
  const evidenceFile = "deploy/openshell/evidence/openshell-runtime-conformance-v2.json";
  const acceptance = {
    schemaVersion: "dataground.dev.openshell-runtime-conformance-acceptance/v1",
    evidence: {
      file: evidenceFile,
      sha256: createHash("sha256").update(evidenceBytes).digest("hex"),
      runID: valid.run.id,
    },
    workflow: {
      path: valid.run.provenance.workflow,
      runID: valid.run.provenance.workflowRunID,
      headCommit: valid.run.provenance.sourceCommit,
    },
    artifact: {
      name: valid.run.provenance.artifactName,
      id: 456,
      archiveDigest: `sha256:${"b".repeat(64)}`,
    },
  };
  const acceptanceCases = [
    ["representative acceptance", acceptance, true],
    [
      "evidence digest substitution",
      {
        ...acceptance,
        evidence: { ...acceptance.evidence, sha256: "c".repeat(64) },
      },
      false,
    ],
    [
      "workflow run substitution",
      {
        ...acceptance,
        workflow: { ...acceptance.workflow, runID: acceptance.workflow.runID + 1 },
      },
      false,
    ],
    [
      "artifact extension",
      {
        ...acceptance,
        artifact: { ...acceptance.artifact, producerDigest: "d".repeat(64) },
      },
      false,
    ],
  ];
  failures.push(
    ...acceptanceCases.flatMap(([name, candidate, shouldPass]) => {
      const passed = verifyAcceptance(evidenceBytes, valid, evidenceFile, candidate).length === 0;
      return passed === shouldPass ? [] : [`self-test failed: ${name}`];
    }),
  );
  if (failures.length > 0) {
    throw new Error(failures.join("\n"));
  }
}

async function readJSON(path) {
  return JSON.parse(await readFile(path, "utf8"));
}

const [argument, acceptanceArgument, ...extraArguments] = process.argv.slice(2);
if (argument === "--self-test" && acceptanceArgument === undefined) {
  runSelfTest();
  console.log("OpenShell runtime evidence verifier is internally consistent.");
} else if (argument && extraArguments.length === 0) {
  const evidencePath = resolve(process.cwd(), argument);
  const evidenceBytes = await readFile(evidencePath);
  const evidence = JSON.parse(evidenceBytes.toString("utf8"));
  const failures = verifyEvidence(evidence);
  if (acceptanceArgument) {
    const acceptance = await readJSON(resolve(process.cwd(), acceptanceArgument));
    failures.push(...verifyAcceptance(evidenceBytes, evidence, argument, acceptance));
  }
  if (failures.length > 0) {
    console.error(failures.map((failure) => `- ${failure}`).join("\n"));
    process.exitCode = 1;
  } else if (acceptanceArgument) {
    console.log("OpenShell runtime conformance evidence and acceptance are valid.");
  } else {
    console.log("OpenShell runtime conformance evidence is valid.");
  }
} else {
  console.error(
    "usage: node scripts/check-openshell-runtime-evidence.mjs <evidence.json> [acceptance.json] | --self-test",
  );
  process.exitCode = 2;
}
