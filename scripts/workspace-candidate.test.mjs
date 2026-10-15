import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { test } from "node:test";
import { verifyOfflineAttestationSnapshots } from "./check-codex-candidate-attestation.mjs";
import { verifyPublication } from "./check-codex-candidate-publication.mjs";
import { verifyWorkspacePublication } from "./check-workspace-candidate.mjs";
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
