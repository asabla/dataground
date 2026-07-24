import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

const root = resolve(import.meta.dirname, "..");
const profilePath = resolve(root, "deploy/openshell/development-profile.json");
const schemaPath = resolve(root, "deploy/openshell/credential-non-exposure-evidence.schema.json");
const scanSchemaPath = resolve(root, "deploy/openshell/credential-canary-scan.schema.json");
const requiredSurfaces = [
  "sandbox-process",
  "sandbox-environment",
  "sandbox-filesystem",
  "provider-arguments",
  "gateway-logs",
  "sandbox-logs",
  "runtime-errors",
];

const [profile, schema, scanSchema] = await Promise.all([
  readJSON(profilePath),
  readJSON(schemaPath),
  readJSON(scanSchemaPath),
]);
const ajv = new Ajv2020({ allErrors: true, strict: true });
addFormats(ajv);
const validateSchema = ajv.compile(schema);
const validateScanSchema = ajv.compile(scanSchema);

const expectedProfile = {
  openshellCommit: profile.source.openshell.commit,
  gatewayImage: profile.artifacts.gateway,
  supervisorImage: profile.artifacts.supervisor,
  sandboxImage: profile.artifacts.sandbox,
  providerProfileSourceSHA256: profile.providerProfileEvidence.codex.sha256,
  runtimeVersion: profile.runtime.version,
  gatewayEndpoint: profile.topology.gatewayEndpoint,
  driver: profile.topology.driver,
};
const expectedScanLimits = profile.providerProfileEvidence.contract.scanner.surfaceMaxBytes;

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
    failures.push("evidence does not match the checked-in OpenShell profile");
  }

  const surfaces = evidence.checks.map((check) => check.surface);
  if (
    surfaces.length !== new Set(surfaces).size ||
    JSON.stringify([...surfaces].sort()) !== JSON.stringify([...requiredSurfaces].sort())
  ) {
    failures.push("evidence must contain every inspection surface exactly once");
  }

  if (evidence.checks.some((check) => check.canaryCommitment !== evidence.run.canaryCommitment)) {
    failures.push("every inspection surface must bind to the run canary commitment");
  }

  const runStartedAt = Date.parse(evidence.run.startedAt);
  const runFinishedAt = Date.parse(evidence.run.finishedAt);
  if (
    !Number.isFinite(runStartedAt) ||
    !Number.isFinite(runFinishedAt) ||
    runFinishedAt < runStartedAt
  ) {
    failures.push("evidence timestamps are invalid or out of order");
  }

  for (const check of evidence.checks) {
    if (!validateScanSchema(check)) {
      failures.push(`${check.surface} is not a complete canary scanner report`);
      continue;
    }
    if (check.inputLimitBytes !== expectedScanLimits[check.surface]) {
      failures.push(`${check.surface} does not use the checked-in input limit`);
    }
    if (check.inspectedBytes > check.inputLimitBytes) {
      failures.push(`${check.surface} claims more inspected bytes than its input limit`);
    }
    const scanStartedAt = Date.parse(check.startedAt);
    const scanFinishedAt = Date.parse(check.finishedAt);
    if (
      !Number.isFinite(scanStartedAt) ||
      !Number.isFinite(scanFinishedAt) ||
      scanFinishedAt < scanStartedAt ||
      scanStartedAt < runStartedAt ||
      scanFinishedAt > runFinishedAt
    ) {
      failures.push(`${check.surface} observation window is outside the evidence run`);
    }
  }

  return failures;
}

function representativeEvidence() {
  return {
    schemaVersion: "dataground.dev.openshell-credential-non-exposure-evidence/v1",
    profile: structuredClone(expectedProfile),
    run: {
      startedAt: "2026-07-24T12:00:00.000Z",
      finishedAt: "2026-07-24T12:01:00.000Z",
      verifier: { name: "dataground-openshell-canary", version: "1.0.0" },
      canaryCommitment: `sha256:${"a".repeat(64)}`,
    },
    checks: requiredSurfaces.map((surface) => ({
      schemaVersion: "dataground.dev.openshell-canary-scan/v1",
      surface,
      canaryCommitment: `sha256:${"a".repeat(64)}`,
      status: "clear",
      matches: 0,
      complete: true,
      inputLimitBytes: expectedScanLimits[surface],
      inspectedBytes: 128,
      candidates: 0,
      startedAt: "2026-07-24T12:00:10.000Z",
      finishedAt: "2026-07-24T12:00:11.000Z",
    })),
    cleanup: {
      sandbox: "removed",
      providerBinding: "removed",
      workspace: "removed",
    },
    result: "passed",
  };
}

function runSelfTest() {
  const valid = representativeEvidence();
  const cases = [
    ["representative evidence", valid, true],
    ["profile drift", { ...valid, profile: { ...valid.profile, runtimeVersion: "0.0.0" } }, false],
    [
      "duplicate surface",
      {
        ...valid,
        checks: valid.checks.map((check, index) =>
          index === 1 ? { ...check, surface: valid.checks[0].surface } : check,
        ),
      },
      false,
    ],
    [
      "mixed canary commitment",
      {
        ...valid,
        checks: valid.checks.map((check, index) =>
          index === 0 ? { ...check, canaryCommitment: `sha256:${"b".repeat(64)}` } : check,
        ),
      },
      false,
    ],
    [
      "positive match",
      {
        ...valid,
        checks: valid.checks.map((check, index) =>
          index === 0 ? { ...check, matches: 1 } : check,
        ),
      },
      false,
    ],
    [
      "incomplete scan",
      {
        ...valid,
        checks: valid.checks.map((check, index) =>
          index === 0 ? { ...check, complete: false } : check,
        ),
      },
      false,
    ],
    [
      "missing scan metric",
      {
        ...valid,
        checks: valid.checks.map((check, index) =>
          index === 0 ? { ...check, inspectedBytes: undefined } : check,
        ),
      },
      false,
    ],
    [
      "profile limit drift",
      {
        ...valid,
        checks: valid.checks.map((check, index) =>
          index === 0 ? { ...check, inputLimitBytes: check.inputLimitBytes - 1 } : check,
        ),
      },
      false,
    ],
    [
      "impossible inspected size",
      {
        ...valid,
        checks: valid.checks.map((check, index) =>
          index === 0 ? { ...check, inspectedBytes: check.inputLimitBytes + 1 } : check,
        ),
      },
      false,
    ],
    [
      "stale scan window",
      {
        ...valid,
        checks: valid.checks.map((check, index) =>
          index === 0 ? { ...check, startedAt: "2026-07-24T11:59:59.000Z" } : check,
        ),
      },
      false,
    ],
    [
      "reversed scan window",
      {
        ...valid,
        checks: valid.checks.map((check, index) =>
          index === 0
            ? {
                ...check,
                startedAt: "2026-07-24T12:00:12.000Z",
                finishedAt: "2026-07-24T12:00:11.000Z",
              }
            : check,
        ),
      },
      false,
    ],
    ["uncertain cleanup", { ...valid, cleanup: { ...valid.cleanup, sandbox: "unknown" } }, false],
    [
      "reversed timestamps",
      {
        ...valid,
        run: {
          ...valid.run,
          startedAt: "2026-07-24T12:02:00.000Z",
          finishedAt: "2026-07-24T12:01:00.000Z",
        },
      },
      false,
    ],
  ];

  const failures = cases.flatMap(([name, evidence, shouldPass]) => {
    const passed = verifyEvidence(evidence).length === 0;
    return passed === shouldPass ? [] : [`self-test failed: ${name}`];
  });
  const clearScan = {
    schemaVersion: "dataground.dev.openshell-canary-scan/v1",
    surface: "sandbox-process",
    canaryCommitment: `sha256:${"a".repeat(64)}`,
    status: "clear",
    matches: 0,
    complete: true,
    inputLimitBytes: 1024,
    inspectedBytes: 128,
    candidates: 0,
    startedAt: "2026-07-24T12:00:10.000Z",
    finishedAt: "2026-07-24T12:00:11.000Z",
  };
  if (!validateScanSchema(clearScan)) {
    failures.push("self-test failed: representative canary scan report");
  }
  if (validateScanSchema({ ...clearScan, matches: 1 })) {
    failures.push("self-test failed: inconsistent clear canary scan report");
  }
  if (validateScanSchema({ ...clearScan, canaryCommitment: undefined })) {
    failures.push("self-test failed: unbound canary scan report");
  }
  if (validateScanSchema({ ...clearScan, inputLimitBytes: undefined })) {
    failures.push("self-test failed: unbounded canary scan report");
  }
  if (validateScanSchema({ ...clearScan, inputLimitBytes: 268_435_457 })) {
    failures.push("self-test failed: oversized canary scan report");
  }
  if (validateScanSchema({ ...clearScan, startedAt: "2026-07-24T12:00:10+00:00" })) {
    failures.push("self-test failed: non-canonical canary scan timestamp");
  }

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
  console.log("OpenShell credential evidence verifier is internally consistent.");
} else if (argument) {
  const evidence = await readJSON(resolve(process.cwd(), argument));
  const failures = verifyEvidence(evidence);
  if (failures.length > 0) {
    console.error(failures.map((failure) => `- ${failure}`).join("\n"));
    process.exitCode = 1;
  } else {
    console.log("OpenShell credential non-exposure evidence is valid.");
  }
} else {
  console.error(
    "usage: node scripts/check-openshell-credential-evidence.mjs <evidence.json> | --self-test",
  );
  process.exitCode = 2;
}
