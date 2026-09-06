import assert from "node:assert/strict";

export const sourceCommit = "1".repeat(40);
export const digest = `sha256:${"2".repeat(64)}`;
export const imageId = `sha256:${"3".repeat(64)}`;
export const repository = "asabla/dataground";
export const repositoryURL = `https://github.com/${repository}`;
export const imageRepository = "ghcr.io/asabla/dataground-codex-candidate";
export const workflow = ".github/workflows/codex-compatibility.yml";
export const signer = `${repositoryURL}/${workflow}@refs/heads/main`;
export const invocation = `${repositoryURL}/actions/runs/42/attempts/1`;
export const scope = { digest, sourceCommit, runId: "42", runAttempt: "1", architecture: "arm64" };

export function fixture() {
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
