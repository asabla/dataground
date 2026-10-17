import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { test } from "node:test";
import { verifyOfflineAttestationSnapshots } from "./check-codex-candidate-attestation.mjs";
import { verifyPublication } from "./check-codex-candidate-publication.mjs";
import { verifyWorkspacePublication } from "./check-workspace-candidate.mjs";
import { verifyWorkspaceExportPublication } from "./check-workspace-export-candidate.mjs";
import { fixture, scope } from "./codex-candidate-fixture.mjs";

const contractLabel = "dataground.dev.workspace-source-contract";
const revisionLabel = "dataground.dev.workspace-source-revision";
const hash = (value) => createHash("sha256").update(value).digest("hex");
function workspaceFixture() {
  const f = fixture();
  f.image.Config.Labels[contractLabel] = "dataground.workspace-source-materialization/v1";
  f.image.Config.Labels[revisionLabel] = scope.sourceCommit;
  return f;
}

test("workspace publication requires signed helper capability from the same source", () => {
  const f = workspaceFixture();
  const result = verifyWorkspacePublication(scope, f.run);
  assert.equal(result.candidateProfile, "openshell-codex-workspace-candidate/v1");
  assert.equal(result.sourceCommit, scope.sourceCommit);
  assert.equal(result.certificationEligible, false);
  assert.throws(() => verifyWorkspacePublication(scope, fixture().run));
  // Older source-free consumers remain valid for their original capability.
  assert.equal(verifyPublication(scope, fixture().run).certificationEligible, false);
});

test("workspace publication rejects changed helper metadata and incomplete evidence", () => {
  for (const mutate of [
    (f) => {
      delete f.image.Config.Labels[contractLabel];
    },
    (f) => {
      f.image.Config.Labels[contractLabel] = "future-contract";
    },
    (f) => {
      f.image.Config.Labels[revisionLabel] = "0".repeat(40);
    },
    (f) => {
      f.image.Config.User = "root";
    },
    (f) => {
      f.attempt.conclusion = "failure";
    },
    (f) => {
      f.jobs.jobs[0].conclusion = "failure";
    },
    (f) => {
      f.certificate.sourceRepositoryDigest = "0".repeat(40);
    },
  ]) {
    const f = workspaceFixture();
    mutate(f);
    assert.throws(() => verifyWorkspacePublication(scope, f.run));
  }
});

function offlineFixture(mutate = () => {}) {
  const f = workspaceFixture();
  mutate(f.image.Config);
  const imageConfig = Buffer.from(
    JSON.stringify({ architecture: "arm64", os: "linux", config: f.image.Config }),
  );
  const manifest = Buffer.from(
    JSON.stringify({
      schemaVersion: 2,
      mediaType: "application/vnd.oci.image.manifest.v1+json",
      config: {
        mediaType: "application/vnd.oci.image.config.v1+json",
        digest: `sha256:${hash(imageConfig)}`,
        size: imageConfig.length,
      },
    }),
  );
  const expected = { ...scope, digest: `sha256:${hash(manifest)}` };
  f.result.statement.subject[0].digest.sha256 = expected.digest.slice(7);
  const trustedRoot = Buffer.from("synthetic roots");
  return {
    f,
    expected,
    inputs: {
      manifest,
      imageConfig,
      bundle: Buffer.from("synthetic bundle"),
      trustedRoot,
      trustedRootSHA256: hash(trustedRoot),
    },
  };
}

test("offline workspace verification binds helper metadata through the signed configuration", () => {
  const { f, expected, inputs } = offlineFixture();
  const result = verifyOfflineAttestationSnapshots(expected, inputs, f.run, "codex-workspace");
  assert.equal(result.candidateProfile, "openshell-codex-workspace-candidate/v1");
  assert.equal(result.image.configDigest, `sha256:${hash(inputs.imageConfig)}`);
  assert.equal(result.publicationCompletionChecked, false);
  assert.equal(result.certificationEligible, false);
});

test("offline source capability cannot omit configuration or substitute helper identity", () => {
  for (const kind of ["missing-config", "missing-label", "wrong-source", "changed-config"]) {
    const { f, expected, inputs } = offlineFixture((config) => {
      if (kind === "missing-label") delete config.Labels[contractLabel];
      if (kind === "wrong-source") config.Labels[revisionLabel] = "0".repeat(40);
    });
    if (kind === "missing-config") delete inputs.imageConfig;
    if (kind === "changed-config") inputs.imageConfig = Buffer.from("substituted");
    assert.throws(() =>
      verifyOfflineAttestationSnapshots(expected, inputs, f.run, "codex-workspace"),
    );
    assert.equal(f.calls.length, 0);
  }
});

const exportLabels = {
  "dataground.dev.workspace-change-contract": "dataground.workspace-changes/v1",
  "dataground.dev.native-owner-contract": "dataground.native-owner/v1",
  "dataground.dev.workspace-export-revision": scope.sourceCommit,
};
function exportFixture() {
  const f = workspaceFixture();
  Object.assign(f.image.Config.Labels, exportLabels);
  return f;
}

test("export publication requires all helper contracts from the same revision", () => {
  const result = verifyWorkspaceExportPublication(scope, exportFixture().run);
  assert.equal(result.candidateProfile, "openshell-codex-workspace-export-candidate/v1");
  assert.equal(result.certificationEligible, false);
  assert.throws(() => verifyWorkspaceExportPublication(scope, workspaceFixture().run));
  assert.equal(
    verifyWorkspacePublication(scope, exportFixture().run).candidateProfile,
    "openshell-codex-workspace-candidate/v1",
  );
  for (const label of [contractLabel, revisionLabel, ...Object.keys(exportLabels)]) {
    for (const value of [undefined, "substituted"]) {
      const f = exportFixture();
      f.image.Config.Labels[label] = value;
      assert.throws(() => verifyWorkspaceExportPublication(scope, f.run), label);
    }
  }
});

test("offline export verification binds the complete helper set through the signed manifest", () => {
  const { f, expected, inputs } = offlineFixture((config) =>
    Object.assign(config.Labels, exportLabels),
  );
  const result = verifyOfflineAttestationSnapshots(
    expected,
    inputs,
    f.run,
    "codex-workspace-export",
  );
  assert.equal(result.candidateProfile, "openshell-codex-workspace-export-candidate/v1");
  assert.equal(result.image.configDigest, `sha256:${hash(inputs.imageConfig)}`);
  assert.equal(result.publicationCompletionChecked, false);
  assert.equal(result.certificationEligible, false);
});

test("offline export verification rejects missing, substituted and source-only helpers before verification", () => {
  for (const label of [contractLabel, revisionLabel, ...Object.keys(exportLabels)]) {
    for (const value of [undefined, "substituted"]) {
      const { f, expected, inputs } = offlineFixture((config) => {
        Object.assign(config.Labels, exportLabels);
        config.Labels[label] = value;
      });
      assert.throws(() =>
        verifyOfflineAttestationSnapshots(expected, inputs, f.run, "codex-workspace-export"),
      );
      assert.equal(f.calls.length, 0);
    }
  }
  for (const kind of ["no-config", "changed-config", "source-only"]) {
    const { f, expected, inputs } = offlineFixture((config) => {
      if (kind !== "source-only") Object.assign(config.Labels, exportLabels);
    });
    if (kind === "no-config") delete inputs.imageConfig;
    if (kind === "changed-config") inputs.imageConfig = Buffer.from("substituted");
    assert.throws(() =>
      verifyOfflineAttestationSnapshots(expected, inputs, f.run, "codex-workspace-export"),
    );
    assert.equal(f.calls.length, 0);
  }
});
