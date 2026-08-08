import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import { relative, resolve } from "node:path";

import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

const root = resolve(import.meta.dirname, "..");
const profileFile = "deploy/openshell/development-profile.json";
const schemaFile = "deploy/openshell/runtime-certification-manifest.schema.json";
const evidenceVerifier = resolve(root, "scripts/check-openshell-runtime-evidence.mjs");
const maximumValidityMilliseconds = 30 * 24 * 60 * 60 * 1000;
const maximumClockSkewMilliseconds = 5 * 60 * 1000;

const [profileBytes, schema] = await Promise.all([
  readFile(resolve(root, profileFile)),
  readJSON(resolve(root, schemaFile)),
]);
const profile = JSON.parse(profileBytes.toString("utf8"));
const ajv = new Ajv2020({ allErrors: true, strict: true });
addFormats(ajv);
const validateSchema = ajv.compile(schema);

function digest(bytes) {
  return createHash("sha256").update(bytes).digest("hex");
}

function canonicalJSON(value) {
  return `${JSON.stringify(sortJSON(value))}\n`;
}

function sortJSON(value) {
  if (Array.isArray(value)) {
    return value.map(sortJSON);
  }
  if (value !== null && typeof value === "object") {
    return Object.fromEntries(
      Object.keys(value)
        .sort()
        .map((key) => [key, sortJSON(value[key])]),
    );
  }
  return value;
}

function repositoryPath(path) {
  const absolute = resolve(process.cwd(), path);
  const normalized = relative(root, absolute).replaceAll("\\", "/");
  if (normalized === "" || normalized === ".." || normalized.startsWith("../")) {
    return null;
  }
  return normalized;
}

function parseTimestamp(value) {
  const parsed = Date.parse(value);
  if (!Number.isFinite(parsed) || new Date(parsed).toISOString() !== value) {
    return null;
  }
  return parsed;
}

function sameStringArray(left, right) {
  return (
    Array.isArray(left) &&
    Array.isArray(right) &&
    left.length === right.length &&
    left.every((value, index) => value === right[index])
  );
}

function verifyCertification(
  manifestBytes,
  manifest,
  evidenceBytes,
  evidence,
  acceptanceBytes,
  acceptance,
  expectations,
) {
  const failures = [];
  if (!validateSchema(manifest)) {
    failures.push(
      ...validateSchema.errors.map(
        (error) => `manifest${error.instancePath || "/"} ${error.message}`,
      ),
    );
    return failures;
  }
  if (!Buffer.from(canonicalJSON(manifest)).equals(manifestBytes)) {
    failures.push("runtime certification manifest is not canonical JSON");
  }
  if (digest(manifestBytes) !== expectations.manifestSHA256) {
    failures.push("runtime certification manifest digest does not match");
  }
  if (
    manifest.scope.isolationDomainId !== expectations.isolationDomainId ||
    manifest.scope.serviceId !== expectations.serviceId ||
    manifest.scope.revisionId !== expectations.revisionId
  ) {
    failures.push("runtime certification scope does not match");
  }
  if (
    manifest.runtime.profileId !== expectations.runtimeProfile ||
    manifest.artifacts.sourceRevision !== expectations.sourceRevision
  ) {
    failures.push("runtime certification revision or profile does not match");
  }
  if (
    manifest.generation < expectations.minimumGeneration ||
    expectations.rejectedCertificationIds.has(manifest.certificationId)
  ) {
    failures.push("runtime certification is stale or already consumed");
  }
  if (
    !evidenceBytes ||
    !evidence ||
    !acceptanceBytes ||
    !acceptance ||
    typeof evidence !== "object" ||
    typeof acceptance !== "object"
  ) {
    failures.push("runtime certification evidence is unavailable");
    return failures;
  }

  if (
    manifest.artifacts.profile.file !== profileFile ||
    manifest.artifacts.profile.sha256 !== digest(profileBytes) ||
    manifest.artifacts.profile.schemaVersion !== profile.schemaVersion
  ) {
    failures.push("runtime certification does not bind the checked profile");
  }
  if (
    manifest.runtime.family !== profile.runtime?.id ||
    manifest.runtime.version !== profile.runtime?.version
  ) {
    failures.push("runtime certification does not bind the checked runtime");
  }
  if (
    manifest.artifacts.evidence.sha256 !== digest(evidenceBytes) ||
    manifest.artifacts.evidence.runId !== evidence.run?.id ||
    manifest.artifacts.evidence.file !== acceptance.evidence?.file ||
    manifest.artifacts.evidence.sha256 !== acceptance.evidence?.sha256 ||
    manifest.artifacts.evidence.runId !== acceptance.evidence?.runID
  ) {
    failures.push("runtime certification does not bind the exact evidence record");
  }
  if (
    manifest.artifacts.acceptance.sha256 !== digest(acceptanceBytes) ||
    acceptance.workflow?.headCommit !== manifest.artifacts.sourceRevision ||
    evidence.run?.provenance?.sourceCommit !== manifest.artifacts.sourceRevision ||
    evidence.run?.provenance?.sourceCommit !== acceptance.workflow?.headCommit ||
    evidence.run?.provenance?.workflowRunID !== acceptance.workflow?.runID ||
    evidence.run?.provenance?.workflow !== acceptance.workflow?.path
  ) {
    failures.push("runtime certification does not bind accepted provenance");
  }

  const expectedClassifications = profile.runtime?.conformance?.capabilityClassifications;
  const evidenceCapabilities = new Map(
    Array.isArray(evidence.capabilities)
      ? evidence.capabilities.map((capability) => [capability.name, capability])
      : [],
  );
  const expectedNames = Object.keys(expectedClassifications ?? {}).sort();
  const actualNames = manifest.runtime.capabilities.map((capability) => capability.name);
  if (
    !sameStringArray(actualNames, expectedNames) ||
    evidenceCapabilities.size !== expectedNames.length
  ) {
    failures.push("runtime certification capability set is incomplete or duplicated");
  } else {
    for (const capability of manifest.runtime.capabilities) {
      const evidenceCapability = evidenceCapabilities.get(capability.name);
      if (
        capability.classification !== expectedClassifications[capability.name] ||
        capability.classification !== evidenceCapability?.classification ||
        !sameStringArray(capability.evidence, evidenceCapability?.evidence)
      ) {
        failures.push(
          `runtime certification capability ${capability.name} does not match evidence`,
        );
      }
    }
  }

  const issuedAt = parseTimestamp(manifest.validity.issuedAt);
  const expiresAt = parseTimestamp(manifest.validity.expiresAt);
  const evidenceFinishedAt = Date.parse(evidence.run?.finishedAt);
  if (
    issuedAt === null ||
    expiresAt === null ||
    !Number.isFinite(evidenceFinishedAt) ||
    issuedAt < evidenceFinishedAt ||
    issuedAt > expectations.now + maximumClockSkewMilliseconds ||
    expiresAt <= issuedAt ||
    expiresAt - issuedAt > maximumValidityMilliseconds ||
    expiresAt <= expectations.now
  ) {
    failures.push("runtime certification validity is expired, premature, or too broad");
  }
  return failures;
}

function verifyEvidencePair(evidencePath, acceptancePath) {
  const result = spawnSync(process.execPath, [evidenceVerifier, evidencePath, acceptancePath], {
    cwd: root,
    encoding: "utf8",
    env: {},
    timeout: 60_000,
  });
  if (result.error || result.status !== 0) {
    return ["runtime conformance evidence or acceptance is invalid"];
  }
  return [];
}

function parseArguments(arguments_) {
  if (arguments_.length === 1 && arguments_[0] === "--self-test") {
    return { selfTest: true };
  }
  const [manifestPath, evidencePath, acceptancePath, ...options] = arguments_;
  if (!manifestPath || !evidencePath || !acceptancePath) {
    return null;
  }
  const values = new Map();
  const rejectedCertificationIds = new Set();
  const allowedOptions = new Set([
    "--isolation-domain",
    "--service",
    "--revision",
    "--runtime-profile",
    "--source-revision",
    "--expected-manifest-sha256",
    "--minimum-generation",
    "--reject-certification-id",
  ]);
  for (let index = 0; index < options.length; index += 2) {
    const name = options[index];
    const value = options[index + 1];
    if (!allowedOptions.has(name) || value === undefined) {
      return null;
    }
    if (name === "--reject-certification-id") {
      rejectedCertificationIds.add(value);
    } else if (values.has(name)) {
      return null;
    } else {
      values.set(name, value);
    }
  }
  const minimumGeneration = Number(values.get("--minimum-generation"));
  const parsed = {
    selfTest: false,
    manifestPath,
    evidencePath,
    acceptancePath,
    expectations: {
      isolationDomainId: values.get("--isolation-domain"),
      serviceId: values.get("--service"),
      revisionId: values.get("--revision"),
      runtimeProfile: values.get("--runtime-profile"),
      sourceRevision: values.get("--source-revision"),
      manifestSHA256: values.get("--expected-manifest-sha256"),
      minimumGeneration,
      rejectedCertificationIds,
      now: Date.now(),
    },
  };
  if (
    !parsed.expectations.isolationDomainId ||
    !parsed.expectations.serviceId ||
    !parsed.expectations.revisionId ||
    !parsed.expectations.runtimeProfile ||
    !parsed.expectations.sourceRevision ||
    !/^[a-f0-9]{64}$/.test(parsed.expectations.manifestSHA256 ?? "") ||
    !Number.isSafeInteger(minimumGeneration) ||
    minimumGeneration < 1
  ) {
    return null;
  }
  return parsed;
}

function representativeInputs() {
  const sourceRevision = "a".repeat(40);
  const runId = "b".repeat(32);
  const evidenceFile = "deploy/openshell/evidence/openshell-runtime-conformance-v1.json";
  const acceptanceFile =
    "deploy/openshell/evidence/openshell-runtime-conformance-acceptance-v1.json";
  const classifications = profile.runtime.conformance.capabilityClassifications;
  const evidence = {
    run: {
      id: runId,
      finishedAt: "2026-08-01T00:00:00.000Z",
      provenance: {
        sourceCommit: sourceRevision,
        workflow: ".github/workflows/openshell-runtime-conformance.yml",
        workflowRunID: 42,
      },
    },
    capabilities: Object.keys(classifications)
      .sort()
      .map((name) => ({
        name,
        classification: classifications[name],
        evidence: classifications[name] === "supported" ? ["turn-success"] : [],
      })),
  };
  const evidenceBytes = Buffer.from(canonicalJSON(evidence));
  const acceptance = {
    evidence: {
      file: evidenceFile,
      sha256: digest(evidenceBytes),
      runID: runId,
    },
    workflow: {
      path: evidence.run.provenance.workflow,
      runID: evidence.run.provenance.workflowRunID,
      headCommit: sourceRevision,
    },
  };
  const acceptanceBytes = Buffer.from(canonicalJSON(acceptance));
  const manifest = {
    schemaVersion: "dataground.dev.openshell-runtime-certification-manifest/v1",
    certificationId: "rtcert_0123456789abcdefghij",
    generation: 3,
    scope: {
      isolationDomainId: "iso_0123456789abcdefghij",
      serviceId: "svc_0123456789abcdefghij",
      revisionId: "rev_0123456789abcdefghij",
    },
    runtime: {
      profileId: "openshell-codex-development/v1",
      family: profile.runtime.id,
      version: profile.runtime.version,
      protocol: "codex.app-server/v1",
      capabilityContract: "dataground.runtime-capabilities/v1",
      capabilities: evidence.capabilities,
    },
    artifacts: {
      sourceRevision,
      profile: {
        file: profileFile,
        sha256: digest(profileBytes),
        schemaVersion: profile.schemaVersion,
      },
      evidence: {
        file: evidenceFile,
        sha256: digest(evidenceBytes),
        runId,
      },
      acceptance: {
        file: acceptanceFile,
        sha256: digest(acceptanceBytes),
      },
    },
    validity: {
      issuedAt: "2026-08-02T00:00:00.000Z",
      expiresAt: "2026-08-20T00:00:00.000Z",
      reviewerId: "reviewer_1",
      reason: "Reviewed exact loopback runtime evidence.",
    },
    limitations: {
      deploymentScope: "loopback-development-only",
      productionCertified: false,
      referenceRuntimeDefault: true,
      enforcement: "deny-all-fixture-only",
    },
  };
  const manifestBytes = Buffer.from(canonicalJSON(manifest));
  const expectations = {
    isolationDomainId: manifest.scope.isolationDomainId,
    serviceId: manifest.scope.serviceId,
    revisionId: manifest.scope.revisionId,
    runtimeProfile: manifest.runtime.profileId,
    sourceRevision,
    manifestSHA256: digest(manifestBytes),
    minimumGeneration: 3,
    rejectedCertificationIds: new Set(),
    now: Date.parse("2026-08-03T00:00:00.000Z"),
  };
  return {
    manifest,
    manifestBytes,
    evidence,
    evidenceBytes,
    acceptance,
    acceptanceBytes,
    expectations,
  };
}

function runSelfTest() {
  const valid = representativeInputs();
  const cases = [
    ["representative manifest", valid, true],
    [
      "tampered serialization",
      {
        ...valid,
        manifestBytes: Buffer.concat([valid.manifestBytes, Buffer.from(" ")]),
      },
      false,
    ],
    [
      "expired manifest",
      {
        ...valid,
        expectations: {
          ...valid.expectations,
          now: Date.parse("2026-08-21T00:00:00.000Z"),
        },
      },
      false,
    ],
    [
      "wrong domain",
      {
        ...valid,
        expectations: {
          ...valid.expectations,
          isolationDomainId: "iso_abcdefghij0123456789",
        },
      },
      false,
    ],
    [
      "wrong revision",
      {
        ...valid,
        expectations: {
          ...valid.expectations,
          revisionId: "rev_abcdefghij0123456789",
        },
      },
      false,
    ],
    [
      "wrong profile",
      {
        ...valid,
        expectations: { ...valid.expectations, runtimeProfile: "reference/v1" },
      },
      false,
    ],
    [
      "unknown capability",
      {
        ...valid,
        manifest: {
          ...valid.manifest,
          runtime: {
            ...valid.manifest.runtime,
            capabilities: valid.manifest.runtime.capabilities.map((capability, index) =>
              index === 0 ? { ...capability, name: "unknown" } : capability,
            ),
          },
        },
      },
      false,
    ],
    ["missing evidence", { ...valid, evidence: null, evidenceBytes: null }, false],
    [
      "replayed certification",
      {
        ...valid,
        expectations: {
          ...valid.expectations,
          rejectedCertificationIds: new Set([valid.manifest.certificationId]),
        },
      },
      false,
    ],
    [
      "duplicate capability",
      {
        ...valid,
        manifest: {
          ...valid.manifest,
          runtime: {
            ...valid.manifest.runtime,
            capabilities: [
              ...valid.manifest.runtime.capabilities,
              valid.manifest.runtime.capabilities[0],
            ],
          },
        },
      },
      false,
    ],
    [
      "stale generation",
      {
        ...valid,
        expectations: { ...valid.expectations, minimumGeneration: 4 },
      },
      false,
    ],
  ];
  const failures = cases.flatMap(([name, candidate, shouldPass]) => {
    const passed =
      verifyCertification(
        candidate.manifestBytes,
        candidate.manifest,
        candidate.evidenceBytes,
        candidate.evidence,
        candidate.acceptanceBytes,
        candidate.acceptance,
        candidate.expectations,
      ).length === 0;
    return passed === shouldPass ? [] : [`self-test failed: ${name}`];
  });
  if (failures.length > 0) {
    throw new Error(failures.join("\n"));
  }
}

async function readJSON(path) {
  return JSON.parse(await readFile(path, "utf8"));
}

const parsed = parseArguments(process.argv.slice(2));
if (parsed?.selfTest) {
  runSelfTest();
  console.log("OpenShell runtime certification verifier is internally consistent.");
} else if (parsed) {
  const manifestFile = repositoryPath(parsed.manifestPath);
  const evidenceFile = repositoryPath(parsed.evidencePath);
  const acceptanceFile = repositoryPath(parsed.acceptancePath);
  const pathFailures = [];
  if (!manifestFile || !evidenceFile || !acceptanceFile) {
    pathFailures.push("runtime certification inputs must be repository files");
  }
  const pairFailures =
    pathFailures.length === 0 ? verifyEvidencePair(evidenceFile, acceptanceFile) : [];
  const [manifestBytes, evidenceBytes, acceptanceBytes] =
    pathFailures.length === 0 && pairFailures.length === 0
      ? await Promise.all([
          readFile(resolve(root, manifestFile)),
          readFile(resolve(root, evidenceFile)),
          readFile(resolve(root, acceptanceFile)),
        ])
      : [null, null, null];
  const parseFailures = [];
  const [manifest, evidence, acceptance] = [manifestBytes, evidenceBytes, acceptanceBytes].map(
    (bytes) => {
      if (!bytes) {
        return null;
      }
      try {
        return JSON.parse(bytes.toString("utf8"));
      } catch {
        parseFailures.push("runtime certification input is not valid JSON");
        return null;
      }
    },
  );
  const failures = [
    ...pathFailures,
    ...pairFailures,
    ...parseFailures,
    ...(manifest
      ? verifyCertification(
          manifestBytes,
          manifest,
          evidenceBytes,
          evidence,
          acceptanceBytes,
          acceptance,
          parsed.expectations,
        )
      : []),
  ];
  if (
    manifest &&
    (manifest.artifacts?.evidence?.file !== evidenceFile ||
      manifest.artifacts?.acceptance?.file !== acceptanceFile)
  ) {
    failures.push("runtime certification input paths do not match the manifest");
  }
  if (failures.length > 0) {
    console.error(failures.map((failure) => `- ${failure}`).join("\n"));
    process.exitCode = 1;
  } else {
    console.log("OpenShell runtime certification manifest is valid for the exact target.");
  }
} else {
  console.error(
    "usage: node scripts/check-openshell-runtime-certification.mjs <manifest.json> <evidence.json> <acceptance.json> --isolation-domain <id> --service <id> --revision <id> --runtime-profile <id> --source-revision <sha> --expected-manifest-sha256 <sha256> --minimum-generation <n> [--reject-certification-id <id>] | --self-test",
  );
  process.exitCode = 2;
}
