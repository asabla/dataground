import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

const root = resolve(import.meta.dirname, "..");
const read = async (name) =>
  JSON.parse(await readFile(resolve(root, "deploy/openshell", name), "utf8"));
const [profile, evidenceSchema, diagnosticSchema, policy, rosettaSchema, rosettaPolicy] =
  await Promise.all([
    read("development-profile.json"),
    read("runtime-conformance-evidence.schema.json"),
    read("runtime-candidate-diagnostic.schema.json"),
    readFile(resolve(root, "deploy/openshell/codex-compatibility/runtime-policy.yaml")),
    read("runtime-rosetta-diagnostic.schema.json"),
    readFile(resolve(root, "deploy/openshell/codex-compatibility/rosetta-runtime-policy.yaml")),
  ]);
const ajv = new Ajv2020({ strict: true, allErrors: true });
addFormats(ajv);
ajv.addSchema(evidenceSchema);
const validate = ajv.compile(diagnosticSchema);
const validateRosetta = ajv.compile(rosettaSchema);
const topology = profile.runtime.conformance.topology;
const expectedProfile = {
  openshellCommit: profile.source.openshell.commit,
  gatewayImage: topology.gatewayImage,
  supervisorImage: profile.artifacts.supervisor,
  runtimeVersion: profile.runtime.version,
  runtimeSchemaCanonicalSHA256: profile.runtime.schemaEvidence.canonicalSHA256,
  gatewayEndpoint: topology.gatewayEndpoint,
  driver: topology.driver,
  composeSHA256: topology.composeSHA256,
  gatewayConfigSHA256: topology.gatewayConfigSHA256,
  runtimePolicySHA256: createHash("sha256").update(policy).digest("hex"),
};

// Go emits nanosecond windows; Date.parse alone loses sub-millisecond order.
function nanoseconds(value) {
  const match = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?Z$/.exec(value);
  if (!match) return null;
  const milliseconds = Date.parse(`${match[1]}Z`);
  if (!Number.isFinite(milliseconds)) return null;
  return BigInt(milliseconds) * 1000000n + BigInt((match[2] ?? "").padEnd(9, "0"));
}

export function verifyDiagnostic(value, { sourceCommit, candidateImage }) {
  if (
    !/^[a-f0-9]{40}$/.test(sourceCommit ?? "") ||
    !/^sha256:[a-f0-9]{64}$/.test(candidateImage ?? "")
  ) {
    return ["exact source commit and local candidate image are required"];
  }
  const rosetta = value?.schemaVersion === "dataground.dev.openshell-runtime-diagnostic/v4";
  if (!(rosetta ? validateRosetta : validate)(value))
    return ["record does not match the closed candidate diagnostic schema"];
  const failures = [];
  if (
    value.run.sourceCommit !== sourceCommit ||
    value.profile.sandboxImage !== candidateImage ||
    Object.entries({
      ...expectedProfile,
      runtimePolicySHA256: createHash("sha256")
        .update(rosetta ? rosettaPolicy : policy)
        .digest("hex"),
    }).some(([key, expected]) => value.profile[key] !== expected)
  ) {
    failures.push("diagnostic does not match the expected source, image and checked profile");
  }
  const required = profile.runtime.conformance.requiredChecks;
  if (
    JSON.stringify(value.checks.map((check) => check.name)) !== JSON.stringify(required) ||
    new Set(value.checks.map((check) => check.observationCommitment)).size !== required.length
  ) {
    failures.push(
      "diagnostic cases or observation commitments are incomplete, reordered or repeated",
    );
  }
  const started = nanoseconds(value.run.startedAt);
  const finished = nanoseconds(value.run.finishedAt);
  let previous = started;
  if (started === null || finished === null || finished <= started) {
    failures.push("diagnostic run window is invalid");
  } else {
    for (const check of value.checks) {
      const begin = nanoseconds(check.startedAt);
      const end = nanoseconds(check.finishedAt);
      if (begin === null || end === null || begin < previous || end <= begin || end > finished) {
        failures.push("diagnostic case windows are invalid or overlap");
        break;
      }
      previous = end;
    }
  }
  const prefixes = {
    gateway: "dg-runtime-gateway-",
    sandbox: "dg-runtime-sandbox-",
    provider: "dg-runtime-provider-",
    runtime: "dg-runtime-session-",
    workspace: "dg-runtime-",
  };
  if (
    Object.entries(prefixes).some(
      ([kind, prefix]) => value.run.resources[kind] !== prefix + value.run.id,
    ) ||
    value.cleanup.sandbox.name !== value.run.resources.sandbox ||
    value.cleanup.providerBinding.name !== value.run.resources.provider ||
    value.cleanup.workspace.name !== value.run.resources.workspace
  ) {
    failures.push("diagnostic resources and cleanup do not match the run");
  }
  return failures;
}

if (import.meta.main) {
  const [file, sourceFlag, sourceCommit, imageFlag, candidateImage, ...extra] =
    process.argv.slice(2);
  if (
    !file ||
    sourceFlag !== "--source-commit" ||
    imageFlag !== "--candidate-image" ||
    extra.length
  ) {
    console.error(
      "usage: node scripts/check-openshell-runtime-diagnostic.mjs <record.json> --source-commit <sha> --candidate-image sha256:<id>",
    );
    process.exitCode = 2;
  } else {
    try {
      const bytes = await readFile(file);
      if (bytes.length > 64 << 10) throw new Error("oversized diagnostic");
      const failures = verifyDiagnostic(JSON.parse(bytes.toString("utf8")), {
        sourceCommit,
        candidateImage,
      });
      if (failures.length) {
        console.error(failures.join("\n"));
        process.exitCode = 1;
      } else {
        console.log(
          "Local candidate diagnostic is valid; it is not release-certification evidence.",
        );
      }
    } catch {
      console.error("Diagnostic input could not be read or validated.");
      process.exitCode = 1;
    }
  }
}
