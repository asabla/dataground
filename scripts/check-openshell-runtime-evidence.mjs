import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

const root = resolve(import.meta.dirname, "..");
const profilePath = resolve(root, "deploy/openshell/development-profile.json");
const schemaPath = resolve(root, "deploy/openshell/runtime-conformance-evidence.schema.json");
const requiredChecks = [
  "gateway-ready",
  "sandbox-ready",
  "initialize",
  "turn-success",
  "event-normalization",
  "interrupt",
  "command-approval",
  "file-change-approval",
  "artifact-export",
  "sandbox-teardown",
];
const expectedClassifications = {
  text: "supported",
  "item-activity": "supported",
  interrupt: "supported",
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
  "command-approval": ["command-approval"],
  "file-change-approval": ["file-change-approval"],
  "artifact-export": ["artifact-export"],
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

const [profile, schema] = await Promise.all([readJSON(profilePath), readJSON(schemaPath)]);
const contract = profile.runtime?.conformance;
const ajv = new Ajv2020({ allErrors: true, strict: true });
addFormats(ajv);
const validateSchema = ajv.compile(schema);

const expectedProfile = {
  openshellCommit: profile.source.openshell.commit,
  gatewayImage: profile.artifacts.gateway,
  supervisorImage: profile.artifacts.supervisor,
  sandboxImage: profile.artifacts.sandbox,
  runtimeVersion: profile.runtime.version,
  runtimeSchemaCanonicalSHA256: profile.runtime.schemaEvidence.canonicalSHA256,
  credentialEvidenceSHA256: profile.providerProfileEvidence.contract.acceptedEvidence.sha256,
  gatewayEndpoint: profile.topology.gatewayEndpoint,
  driver: profile.topology.driver,
  composeSHA256: profile.topology.composeSHA256,
  gatewayConfigSHA256: profile.topology.gatewayConfigSHA256,
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
    JSON.stringify([...checks].sort()) !== JSON.stringify([...requiredChecks].sort()) ||
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
  for (const check of evidence.checks) {
    const startedAt = Date.parse(check.startedAt);
    const finishedAt = Date.parse(check.finishedAt);
    if (
      !Number.isFinite(startedAt) ||
      !Number.isFinite(finishedAt) ||
      finishedAt < startedAt ||
      startedAt < runStartedAt ||
      finishedAt > runFinishedAt
    ) {
      failures.push(`${check.name} observation window is outside the evidence run`);
    }
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
    schemaVersion: "dataground.dev.openshell-runtime-conformance-evidence/v1",
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
        artifactID: 456,
        artifactArchiveDigest: `sha256:${"b".repeat(64)}`,
      },
    },
    checks,
    capabilities: Object.entries(expectedClassifications).map(([name, classification]) => ({
      name,
      classification,
      evidence: structuredClone(expectedEvidence[name] ?? []),
      reasonCode: classification === "supported" ? null : "ADAPTER_UNSUPPORTED",
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
      "missing provenance",
      { ...valid, run: { ...valid.run, provenance: undefined } },
      false,
    ],
    [
      "wrong workflow provenance",
      {
        ...valid,
        run: {
          ...valid.run,
          provenance: { ...valid.run.provenance, workflow: ".github/workflows/other.yml" },
        },
      },
      false,
    ],
    [
      "missing runtime case",
      { ...valid, checks: valid.checks.slice(1) },
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
            ? { ...check, observationCommitment: valid.checks[0].observationCommitment }
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
          resources: { ...valid.run.resources, workspace: "dg-runtime-fedcba9876543210fedcba9876543210" },
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
  ];
  const failures = mutations.flatMap(([name, evidence, shouldPass]) => {
    const passed = verifyEvidence(evidence).length === 0;
    return passed === shouldPass ? [] : [`self-test failed: ${name}`];
  });
  if (failures.length > 0) {
    throw new Error(failures.join("\n"));
  }
}

async function readJSON(path) {
  return JSON.parse(await readFile(path, "utf8"));
}

const argument = process.argv[2];
if (argument === "--self-test") {
  runSelfTest();
  console.log("OpenShell runtime evidence verifier is internally consistent.");
} else if (argument) {
  const evidence = await readJSON(resolve(process.cwd(), argument));
  const failures = verifyEvidence(evidence);
  if (failures.length > 0) {
    console.error(failures.map((failure) => `- ${failure}`).join("\n"));
    process.exitCode = 1;
  } else {
    console.log("OpenShell runtime conformance evidence is valid.");
  }
} else {
  console.error(
    "usage: node scripts/check-openshell-runtime-evidence.mjs <evidence.json> | --self-test",
  );
  process.exitCode = 2;
}
