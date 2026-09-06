import assert from "node:assert/strict";
import { test } from "node:test";
import { verifyPublication } from "./check-codex-candidate-publication.mjs";

const sourceCommit = "1".repeat(40);
const digest = `sha256:${"2".repeat(64)}`;
const imageId = `sha256:${"3".repeat(64)}`;
const repository = "asabla/dataground";
const repositoryURL = `https://github.com/${repository}`;
const imageRepository = "ghcr.io/asabla/dataground-codex-candidate";
const workflow = ".github/workflows/codex-compatibility.yml";
const signer = `${repositoryURL}/${workflow}@refs/heads/main`;
const invocation = `${repositoryURL}/actions/runs/42/attempts/1`;
const scope = { digest, sourceCommit, runId: "42", runAttempt: "1", architecture: "arm64" };

function fixture() {
  const certificate = {
    issuer: "https://token.actions.githubusercontent.com",
    subjectAlternativeName: signer,
    buildSignerURI: signer,
    buildSignerDigest: sourceCommit,
    sourceRepositoryURI: repositoryURL,
    sourceRepositoryIdentifier: "1302680759",
    sourceRepositoryOwnerIdentifier: "1063362",
    sourceRepositoryDigest: sourceCommit,
    sourceRepositoryRef: "refs/heads/main",
    buildConfigURI: signer,
    buildConfigDigest: sourceCommit,
    runnerEnvironment: "github-hosted",
    buildTrigger: "workflow_dispatch",
    runInvocationURI: invocation,
  };
  const result = {
    signature: { certificate },
    verifiedTimestamps: [{ type: "Tlog" }],
    statement: {
      _type: "https://in-toto.io/Statement/v1",
      predicateType: "https://slsa.dev/provenance/v1",
      subject: [{ name: imageRepository, digest: { sha256: digest.slice(7) } }],
    },
  };
  const attempt = {
    id: 42,
    run_attempt: 1,
    status: "completed",
    conclusion: "success",
    head_sha: sourceCommit,
    head_branch: "main",
    path: workflow,
    event: "workflow_dispatch",
    repository: { id: 1302680759, full_name: repository },
    head_repository: { id: 1302680759 },
  };
  const jobs = {
    total_count: 2,
    jobs: ["native-sandbox", "publish-candidate"].map((name) => ({
      name,
      status: "completed",
      conclusion: "success",
      run_id: 42,
      run_attempt: 1,
      head_sha: sourceCommit,
    })),
  };
  const image = {
    Id: imageId,
    Os: "linux",
    Architecture: "arm64",
    RepoDigests: [`${imageRepository}@${digest}`],
    Config: {
      User: "sandbox",
      Labels: {
        "org.opencontainers.image.source": repositoryURL,
        "dataground.dev.codex-compatibility-source": "4c70bff480af37b1bf1a9b352b8341060fe55755",
        "dataground.dev.certification-eligible": "false",
      },
    },
  };
  const calls = [];
  const run = (command, args) => {
    calls.push([command, args]);
    if (command === "gh" && args[0] === "--version") return "gh version 2.98.0 (2026-08-20)";
    if (command === "gh" && args[0] === "attestation")
      return JSON.stringify([{ verificationResult: result }]);
    if (command === "gh" && args[0] === "api")
      return JSON.stringify(args[1].endsWith("/jobs?per_page=100") ? jobs : attempt);
    if (command === "docker" && args[0] === "pull") return "";
    if (command === "docker" && args[0] === "image") return JSON.stringify([image]);
    assert.fail("unexpected operation");
  };
  return { certificate, result, attempt, jobs, image, calls, run };
}

test("verifies exact signed publication before pulling the candidate", () => {
  const f = fixture();
  assert.deepEqual(verifyPublication(scope, f.run), {
    image: `${imageRepository}@${digest}`,
    imageId,
    architecture: "arm64",
    sourceCommit,
    invocation,
    certificationEligible: false,
  });
  assert.equal(f.calls[1][1][2], `oci://${imageRepository}@${digest}`);
  for (const flag of [
    "--source-digest",
    "--signer-digest",
    "--source-ref",
    "--signer-workflow",
    "--cert-oidc-issuer",
    "--deny-self-hosted-runners",
  ]) {
    assert.ok(f.calls[1][1].includes(flag));
  }
  assert.equal(f.calls[2][1][1], `repos/${repository}/actions/runs/42/attempts/1`);
  assert.deepEqual(f.calls[4], [
    "docker",
    ["pull", "--quiet", "--platform", "linux/arm64", `${imageRepository}@${digest}`],
  ]);
});

test("rejects changed signer, source, repository, trigger and run bindings before pulling", () => {
  for (const key of Object.keys(fixture().certificate)) {
    const f = fixture();
    f.certificate[key] = "substituted";
    assert.throws(() => verifyPublication(scope, f.run), /No verified attestation/, key);
    assert.equal(
      f.calls.some(([command]) => command === "docker"),
      false,
    );
  }
  for (const mutate of [
    (f) => {
      f.result.statement.subject[0].digest.sha256 = "4".repeat(64);
    },
    (f) => {
      f.result.statement.subject[0].name = "other/image";
    },
    (f) => {
      f.result.statement.subject.push(f.result.statement.subject[0]);
    },
    (f) => {
      f.result.verifiedTimestamps = [];
    },
    (f) => {
      f.result.statement.predicateType = "unverified";
    },
  ]) {
    const f = fixture();
    mutate(f);
    assert.throws(() => verifyPublication(scope, f.run), /No verified attestation/);
  }
});

test("a valid signature cannot accept an incomplete, failed or different workflow attempt", () => {
  for (const [key, value] of [
    ["conclusion", "failure"],
    ["status", "in_progress"],
    ["run_attempt", 2],
    ["id", 43],
    ["head_sha", "4".repeat(40)],
    ["path", "other.yml"],
    ["event", "pull_request"],
    ["head_branch", "other"],
    ["repository", { id: 42, full_name: repository }],
    ["head_repository", { id: 42 }],
  ]) {
    const f = fixture();
    f.attempt[key] = value;
    assert.throws(() => verifyPublication(scope, f.run), /workflow attempt/, key);
    assert.equal(
      f.calls.some(([command]) => command === "docker"),
      false,
    );
  }
  for (const mutate of [
    (f) => {
      f.jobs.jobs[0].conclusion = "skipped";
    },
    (f) => {
      f.jobs.jobs[1].conclusion = "failure";
    },
    (f) => {
      f.jobs.jobs[1].run_attempt = 2;
    },
    (f) => {
      f.jobs.jobs[1].head_sha = "4".repeat(40);
    },
    (f) => {
      f.jobs.jobs[1].name = "native-sandbox";
    },
    (f) => {
      f.jobs.total_count = 3;
    },
  ]) {
    const f = fixture();
    mutate(f);
    assert.throws(() => verifyPublication(scope, f.run), /jobs did not both/);
    assert.equal(
      f.calls.some(([command]) => command === "docker"),
      false,
    );
  }
});

test("rejects architecture and isolation substitution after digest-pinned pull", () => {
  for (const mutate of [
    (f) => {
      f.image.Architecture = "amd64";
    },
    (f) => {
      f.image.Config.User = "root";
    },
    (f) => {
      f.image.RepoDigests = [];
    },
    (f) => {
      f.image.Id = "latest";
    },
    (f) => {
      f.image.Config.Labels["dataground.dev.certification-eligible"] = "true";
    },
    (f) => {
      f.image.Config.Labels["dataground.dev.codex-compatibility-source"] = "4".repeat(40);
    },
  ]) {
    const f = fixture();
    mutate(f);
    assert.throws(() => verifyPublication(scope, f.run), /Pulled candidate image/);
  }
});

test("invalid inputs and failed or malformed verification commands fail closed without leaking output", () => {
  for (const [key, value] of [
    ["digest", "latest"],
    ["runId", "42/other"],
    ["runAttempt", "0"],
    ["sourceCommit", "main"],
    ["architecture", "other"],
  ]) {
    assert.throws(
      () => verifyPublication({ ...scope, [key]: value }, () => assert.fail("unexpected effect")),
      /Exact candidate/,
    );
  }
  for (const operation of ["attestation", "api", "pull", "image"]) {
    const f = fixture();
    assert.throws(
      () =>
        verifyPublication(scope, (command, args) => {
          if (args[0] === operation) throw new Error("sensitive upstream output");
          return f.run(command, args);
        }),
      (error) => !error.message.includes("sensitive"),
    );
  }
  assert.throws(() => verifyPublication(scope, () => "gh version 2.97.0"), /requires GitHub CLI/);
  const f = fixture();
  assert.throws(
    () =>
      verifyPublication(scope, (command, args) =>
        args[0] === "attestation" ? "not JSON" : f.run(command, args),
      ),
    /valid evidence/,
  );
});
