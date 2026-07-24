import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

const root = resolve(import.meta.dirname, "..");
const profilePath = resolve(root, "deploy/openshell/development-profile.json");
const schemaPath = resolve(
  root,
  "deploy/openshell/credential-non-exposure-evidence.schema.json",
);
const requiredSurfaces = [
  "sandbox-process",
  "sandbox-environment",
  "sandbox-filesystem",
  "provider-arguments",
  "gateway-logs",
  "sandbox-logs",
  "runtime-errors",
];

const [profile, schema] = await Promise.all([
  readJSON(profilePath),
  readJSON(schemaPath),
]);
const ajv = new Ajv2020({ allErrors: true, strict: true });
addFormats(ajv);
const validateSchema = ajv.compile(schema);

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

function verifyEvidence(evidence) {
  const failures = [];

  if (!validateSchema(evidence)) {
    failures.push(
      ...validateSchema.errors.map(
        (error) => `${error.instancePath || "/"} ${error.message}`,
      ),
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
    JSON.stringify([...surfaces].sort()) !==
      JSON.stringify([...requiredSurfaces].sort())
  ) {
    failures.push("evidence must contain every inspection surface exactly once");
  }

  const startedAt = Date.parse(evidence.run.startedAt);
  const finishedAt = Date.parse(evidence.run.finishedAt);
  if (
    !Number.isFinite(startedAt) ||
    !Number.isFinite(finishedAt) ||
    finishedAt < startedAt
  ) {
    failures.push("evidence timestamps are invalid or out of order");
  }

  return failures;
}

function representativeEvidence() {
  return {
    schemaVersion:
      "dataground.dev.openshell-credential-non-exposure-evidence/v1",
    profile: structuredClone(expectedProfile),
    run: {
      startedAt: "2026-07-24T12:00:00.000Z",
      finishedAt: "2026-07-24T12:01:00.000Z",
      verifier: { name: "dataground-openshell-canary", version: "1.0.0" },
      canaryCommitment: `sha256:${"a".repeat(64)}`,
    },
    checks: requiredSurfaces.map((surface) => ({
      surface,
      status: "clear",
      matches: 0,
      complete: true,
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
    [
      "profile drift",
      { ...valid, profile: { ...valid.profile, runtimeVersion: "0.0.0" } },
      false,
    ],
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
      "uncertain cleanup",
      { ...valid, cleanup: { ...valid.cleanup, sandbox: "unknown" } },
      false,
    ],
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
