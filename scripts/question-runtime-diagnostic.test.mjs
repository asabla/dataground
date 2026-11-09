import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { verifyDiagnostic } from "./check-openshell-runtime-diagnostic.mjs";

const prior = JSON.parse(
  await readFile(
    new URL(
      "../deploy/openshell/diagnostics/codex-owned-workspace-candidate-arm64-20261107.json",
      import.meta.url,
    ),
    "utf8",
  ),
);
const expected = {
  sourceCommit: prior.run.sourceCommit,
  candidateImage: prior.profile.sandboxImage,
  supervisorImage: prior.profile.supervisorImage,
  workspaceSource: true,
  workspaceExport: true,
  questions: true,
};

// This is a synthetic verifier fixture, never a retained live observation.
function diagnostic() {
  const value = structuredClone(prior);
  value.schemaVersion = "dataground.dev.openshell-runtime-diagnostic/v8";
  value.workspaceExport.ownedSessions = 14;
  value.questions = {
    profile: "openshell-owned-question-diagnostic/v1",
    runId: value.run.id,
    startedAt: value.workspaceSource.finishedAt,
    finishedAt: value.run.startedAt,
    checks: ["explicit-answer-resumption", "unanswered-expiry", "pending-question-interruption"],
  };
  return value;
}

test("question diagnostics require the explicit version, all owned sessions and ordered evidence", () => {
  assert.deepEqual(verifyDiagnostic(diagnostic(), expected), []);
  assert.notEqual(verifyDiagnostic(prior, expected).length, 0);
  assert.notEqual(verifyDiagnostic(diagnostic(), { ...expected, questions: false }).length, 0);
  for (const mutate of [
    (v) => {
      delete v.questions;
    },
    (v) => {
      v.schemaVersion = prior.schemaVersion;
    },
    (v) => {
      v.questions.runId = "f".repeat(32);
    },
    (v) => {
      v.questions.checks.pop();
    },
    (v) => {
      v.questions.checks.reverse();
    },
    (v) => {
      v.questions.prompt = "private content";
    },
    (v) => {
      v.questions.answer = "private content";
    },
    (v) => {
      v.questions.startedAt = v.workspaceSource.startedAt;
    },
    (v) => {
      v.questions.finishedAt = v.run.finishedAt;
    },
    (v) => {
      v.questions.finishedAt = v.questions.startedAt;
    },
    (v) => {
      v.questions.startedAt = new Date(Date.parse(v.questions.finishedAt) - 360001).toISOString();
    },
    (v) => {
      v.workspaceExport.ownedSessions = 11;
    },
    (v) => {
      v.workspaceExport.ownedSessions++;
    },
    (v) => {
      v.certificationEligible = true;
    },
    (v) => {
      delete v.workspaceSource;
    },
    (v) => {
      delete v.workspaceExport;
    },
  ]) {
    const value = diagnostic();
    mutate(value);
    assert.notEqual(verifyDiagnostic(value, expected).length, 0);
  }
});
