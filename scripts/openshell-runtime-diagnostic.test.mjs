import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

const readJSON = async (name) =>
  JSON.parse(await readFile(new URL(`../deploy/openshell/${name}`, import.meta.url), "utf8"));
const [schema, evidence, profile, candidateSchema] = await Promise.all([
  readJSON("runtime-diagnostic.schema.json"),
  readJSON("runtime-conformance-evidence.schema.json"),
  readJSON("development-profile.json"),
  readJSON("runtime-candidate-diagnostic.schema.json"),
]);
const ajv = new Ajv2020({ strict: true, allErrors: true });
addFormats(ajv);
ajv.addSchema(evidence);
const validate = ajv.compile(schema);
const validateEvidence = ajv.getSchema(evidence.$id);
const validateCandidate = ajv.compile(candidateSchema);
const runID = "1".repeat(32);
const time = "2026-09-05T12:00:00Z";
const topology = profile.runtime.conformance.topology;
const resources = Object.fromEntries(
  Object.entries({
    gateway: "dg-runtime-gateway-",
    sandbox: "dg-runtime-sandbox-",
    provider: "dg-runtime-provider-",
    runtime: "dg-runtime-session-",
    workspace: "dg-runtime-",
  }).map(([key, prefix]) => [key, prefix + runID]),
);
function diagnostic() {
  return {
    schemaVersion: "dataground.dev.openshell-runtime-diagnostic/v1",
    certificationEligible: false,
    profile: {
      openshellCommit: profile.source.openshell.commit,
      gatewayImage: topology.gatewayImage,
      supervisorImage: profile.artifacts.supervisor,
      sandboxImage: profile.artifacts.sandbox,
      runtimeVersion: profile.runtime.version,
      runtimeSchemaCanonicalSHA256: profile.runtime.schemaEvidence.canonicalSHA256,
      credentialEvidenceSHA256: profile.providerProfileEvidence.contract.acceptedEvidence.sha256,
      gatewayEndpoint: topology.gatewayEndpoint,
      driver: topology.driver,
      composeSHA256: topology.composeSHA256,
      gatewayConfigSHA256: topology.gatewayConfigSHA256,
    },
    run: {
      id: runID,
      resources,
      startedAt: time,
      finishedAt: time,
      origin: "local",
      sourceCommit: "1".repeat(40),
      model: "selected-model.v1",
    },
    checks: evidence.$defs.check.properties.name.enum.map((name) => ({
      name,
      status: "passed",
      startedAt: time,
      finishedAt: time,
      observationCommitment: `sha256:${"1".repeat(64)}`,
      nativeProtocolExposed: false,
      upstreamEndpointExposed: false,
    })),
    cleanup: {
      sandbox: { name: resources.sandbox, status: "removed" },
      providerBinding: { name: resources.provider, status: "removed" },
      workspace: { name: resources.workspace, status: "removed" },
    },
    result: "passed",
  };
}

test("local diagnostic reports have a closed shape and cannot be CI evidence", () => {
  const value = diagnostic();
  assert.equal(validate(value), true, JSON.stringify(validate.errors));
  assert.equal(validateEvidence(value), false);
  for (const mutate of [
    (value) => {
      value.certificationEligible = true;
    },
    (value) => {
      value.run.workflowRunID = 42;
    },
    (value) => {
      value.run.origin = "github";
    },
    (value) => {
      value.run.model = "provider/model";
    },
    (value) => {
      value.run.model = "";
    },
    (value) => {
      value.capabilities = [];
    },
    (value) => {
      value.checks.pop();
    },
    (value) => {
      value.cleanup.sandbox.status = "unknown";
    },
    (value) => {
      value.run.credentials = "must never be emitted";
    },
  ]) {
    const invalid = diagnostic();
    mutate(invalid);
    assert.equal(validate(invalid), false);
  }
});

function candidateDiagnostic() {
  const value = diagnostic();
  value.schemaVersion = "dataground.dev.openshell-runtime-diagnostic/v3";
  value.candidateCredentialCheck = "passed";
  value.profile.runtimePolicySHA256 =
    "d7f510e5332068cea5106de5351973dc60f15e22e970fa9352a75d3bbd32b95d";
  value.profile.sandboxImage = `sha256:${"a".repeat(64)}`;
  delete value.profile.credentialEvidenceSHA256;
  return value;
}

test("candidate reports require exact local image and scan without stock certification claims", () => {
  const value = candidateDiagnostic();
  assert.equal(validateCandidate(value), true, JSON.stringify(validateCandidate.errors));
  assert.equal(validate(value), false);
  assert.equal(validateEvidence(value), false);
  for (const mutate of [
    (value) => {
      delete value.profile.runtimePolicySHA256;
    },
    (value) => {
      value.profile.runtimePolicySHA256 = "0".repeat(64);
    },
    (value) => {
      value.schemaVersion = "dataground.dev.openshell-runtime-diagnostic/v2";
    },
    (value) => {
      value.profile.sandboxImage = "candidate:latest";
    },
    (value) => {
      value.profile.credentialEvidenceSHA256 = "1".repeat(64);
    },
    (value) => {
      delete value.candidateCredentialCheck;
    },
    (value) => {
      value.candidateCredentialCheck = "skipped";
    },
    (value) => {
      value.certificationEligible = true;
    },
    (value) => {
      value.run.workflowRunID = 42;
    },
    (value) => {
      value.checks.pop();
    },
    (value) => {
      value.cleanup.sandbox.status = "unknown";
    },
  ]) {
    const invalid = candidateDiagnostic();
    mutate(invalid);
    assert.equal(validateCandidate(invalid), false);
  }
});
