import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { verifyDiagnostic } from "./check-openshell-runtime-diagnostic.mjs";

const bytes = await readFile(
  new URL("../deploy/openshell/diagnostics/codex-candidate-arm64-20260906.json", import.meta.url),
);
const record = JSON.parse(bytes.toString("utf8"));
const expected = {
  sourceCommit: "fed7191b2543c53842cb8310149ff3c359e8c6f5",
  candidateImage: "sha256:703abdf5d88c6298423ba25cb11340990169b4f535b1b75ecc9fb4b730165573",
};

test("the archived ARM64 run retains its exact diagnostic bytes and profile", () => {
  assert.equal(
    createHash("sha256").update(bytes).digest("hex"),
    "4ff6e86de99891700d312c19f42aee4c5c69000110a54070a2f805a44209f665",
  );
  assert.deepEqual(verifyDiagnostic(record, expected), []);
  assert.equal(record.certificationEligible, false);
});

test("the published ARM64 candidate retains its exact local conformance record", async () => {
  const publishedBytes = await readFile(
    new URL(
      "../deploy/openshell/diagnostics/codex-published-candidate-arm64-20260906.json",
      import.meta.url,
    ),
  );
  assert.equal(
    createHash("sha256").update(publishedBytes).digest("hex"),
    "f9fa0ab4183cc3d6d588160da3ec9523c6858c4da0955d97b0e2abb83f59f7aa",
  );
  const published = JSON.parse(publishedBytes.toString("utf8"));
  assert.deepEqual(
    verifyDiagnostic(published, {
      sourceCommit: "e7a0839bfcaa6a9d95540224a63c02be45bb89e1",
      candidateImage: "sha256:9fff9875097a3608fce25e0d401cacc70ad10113237683fe907e45d94e4b24a1",
    }),
    [],
  );
  assert.equal(published.certificationEligible, false);
});

test("candidate diagnostic verification rejects substitution, replay and certification claims", () => {
  const mutations = [
    (v) => {
      v.certificationEligible = true;
    },
    (v) => {
      v.run.workflowRunID = 42;
    },
    (v) => {
      v.run.sourceCommit = "a".repeat(40);
    },
    (v) => {
      v.profile.sandboxImage = `sha256:${"a".repeat(64)}`;
    },
    (v) => {
      v.profile.runtimePolicySHA256 = "a".repeat(64);
    },
    (v) => {
      v.profile.runtimeVersion = "0.0.0";
    },
    (v) => {
      v.checks.reverse();
    },
    (v) => {
      v.checks[1].observationCommitment = v.checks[0].observationCommitment;
    },
    (v) => {
      v.checks[0].finishedAt = v.checks[0].startedAt;
    },
    (v) => {
      v.checks[1].startedAt = v.checks[0].startedAt;
    },
    (v) => {
      v.checks.at(-1).finishedAt = "2027-01-01T00:00:00Z";
    },
    (v) => {
      v.run.finishedAt = v.run.startedAt;
    },
    (v) => {
      v.run.resources.runtime = `dg-runtime-session-${"a".repeat(32)}`;
    },
    (v) => {
      v.cleanup.sandbox.name = `dg-runtime-sandbox-${"a".repeat(32)}`;
    },
  ];
  for (const mutate of mutations) {
    const changed = structuredClone(record);
    mutate(changed);
    assert.notEqual(verifyDiagnostic(changed, expected).length, 0);
  }
  assert.notEqual(verifyDiagnostic(record, { ...expected, sourceCommit: "" }).length, 0);
  assert.notEqual(
    verifyDiagnostic(record, { ...expected, candidateImage: "candidate:latest" }).length,
    0,
  );
});

test("sub-millisecond windows preserve exact chronology", () => {
  const value = structuredClone(record);
  value.run.startedAt = "2026-09-06T08:38:00.000000001Z";
  value.checks.forEach((check, index) => {
    check.startedAt = `2026-09-06T08:38:00.${String(index * 2 + 1).padStart(9, "0")}Z`;
    check.finishedAt = `2026-09-06T08:38:00.${String(index * 2 + 2).padStart(9, "0")}Z`;
  });
  assert.deepEqual(verifyDiagnostic(value, expected), []);
  value.checks[1].startedAt = "2026-09-06T08:38:00.000000001Z";
  assert.notEqual(verifyDiagnostic(value, expected).length, 0);
});

test("Rosetta diagnostics bind a separate policy, compiler input and closed version", () => {
  const value = structuredClone(record);
  value.schemaVersion = "dataground.dev.openshell-runtime-diagnostic/v4";
  value.profile.runtimePolicySHA256 =
    "a1d56c0470c3264c4c37183352d783ebb67911d92ef2eb6ec5f7c76c61f69f39";
  value.policySource = {
    profile: "rosetta-development/v1",
    compilerSourceCommit: "320158f1e4a4eea378d82c1527f4a7af5fb9855b",
    inputSHA256: "b2895b9172c50ba7a5fdf574cebdf6789258cc8ce9f90ce5ad8f2b1ff0a825ab",
  };
  assert.deepEqual(verifyDiagnostic(value, expected), []);
  for (const mutate of [
    (v) => {
      v.schemaVersion = record.schemaVersion;
    },
    (v) => {
      v.profile.runtimePolicySHA256 = record.profile.runtimePolicySHA256;
    },
    (v) => {
      delete v.policySource;
    },
    (v) => {
      v.policySource.compilerSourceCommit = "0".repeat(40);
    },
    (v) => {
      v.policySource.inputSHA256 = "0".repeat(64);
    },
    (v) => {
      v.policySource.profile = "other";
    },
    (v) => {
      v.policySource.extra = true;
    },
    (v) => {
      v.certificationEligible = true;
    },
    (v) => {
      v.checks.pop();
    },
    (v) => {
      v.cleanup.sandbox.name = "other";
    },
  ]) {
    const changed = structuredClone(value);
    mutate(changed);
    assert.notEqual(verifyDiagnostic(changed, expected).length, 0);
  }
});

test("supervisor diagnostics require independent image and realized topology bindings", async () => {
  const value = structuredClone(record);
  const supervisorImage = `sha256:${"b".repeat(64)}`;
  value.schemaVersion = "dataground.dev.openshell-runtime-diagnostic/v5";
  value.profile.runtimePolicySHA256 =
    "a1d56c0470c3264c4c37183352d783ebb67911d92ef2eb6ec5f7c76c61f69f39";
  value.policySource = {
    profile: "rosetta-development/v1",
    compilerSourceCommit: "320158f1e4a4eea378d82c1527f4a7af5fb9855b",
    inputSHA256: "b2895b9172c50ba7a5fdf574cebdf6789258cc8ce9f90ce5ad8f2b1ff0a825ab",
  };
  value.supervisorCandidate = {
    profile: "openshell-supervisor-candidate/v1",
    sourceCommit: "d556748771c41cbbd4e4dd7cd9030c798afe2b7d",
    patchSHA256: "5e97724dd9d9e7fad9abed8a46b9a4d6e06979119998c411daf34b2423056057",
  };
  const template = await readFile(
    new URL("../deploy/openshell/runtime-conformance/gateway.toml", import.meta.url),
    "utf8",
  );
  value.profile.gatewayConfigSHA256 = createHash("sha256")
    .update(template.replace(value.profile.supervisorImage, supervisorImage))
    .digest("hex");
  value.profile.supervisorImage = supervisorImage;
  const selected = { ...expected, supervisorImage };
  assert.deepEqual(verifyDiagnostic(value, selected), []);
  assert.notEqual(verifyDiagnostic(value, expected).length, 0);
  assert.notEqual(verifyDiagnostic(record, selected).length, 0);
  for (const mutate of [
    (v) => {
      v.profile.supervisorImage = `sha256:${"c".repeat(64)}`;
    },
    (v) => {
      v.profile.gatewayConfigSHA256 = record.profile.gatewayConfigSHA256;
    },
    (v) => {
      v.supervisorCandidate.patchSHA256 = "0".repeat(64);
    },
    (v) => {
      v.supervisorCandidate.sourceCommit = "0".repeat(40);
    },
    (v) => {
      delete v.supervisorCandidate;
    },
    (v) => {
      v.schemaVersion = "dataground.dev.openshell-runtime-diagnostic/v4";
    },
    (v) => {
      v.checks.pop();
    },
    (v) => {
      v.certificationEligible = true;
    },
  ]) {
    const changed = structuredClone(value);
    mutate(changed);
    assert.notEqual(verifyDiagnostic(changed, selected).length, 0);
  }
});
