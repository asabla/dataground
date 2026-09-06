import { execFileSync } from "node:child_process";

const repository = "asabla/dataground";
const repositoryURL = `https://github.com/${repository}`;
const imageRepository = "ghcr.io/asabla/dataground-codex-candidate";
const workflow = ".github/workflows/codex-compatibility.yml";
const signer = `${repositoryURL}/${workflow}@refs/heads/main`;
const upstreamCommit = "4c70bff480af37b1bf1a9b352b8341060fe55755";
const digestPattern = /^sha256:[a-f0-9]{64}$/;

function requireCondition(condition, message) {
  if (!condition) throw new Error(message);
}

function execute(command, args) {
  return execFileSync(command, args, {
    encoding: "utf8",
    timeout: command === "docker" ? 600000 : 120000,
    maxBuffer: 8 << 20,
    stdio: ["ignore", "pipe", "pipe"],
  });
}

function commands(run) {
  const invoke = (command, args) => {
    try {
      return run(command, args);
    } catch {
      throw new Error(`${command} verification operation failed; upstream output withheld.`);
    }
  };
  const parse = (command, args) => {
    try {
      return JSON.parse(invoke(command, args));
    } catch {
      throw new Error(`${command} verification operation did not return valid evidence.`);
    }
  };
  return { invoke, parse };
}

// Verification results are consumed only from a successful gh invocation, never
// from caller-supplied JSON that could impersonate signature verification.
export function verifyCandidateAttestation(scope, run = execute, files) {
  const { digest, sourceCommit, runId, runAttempt } = scope;
  requireCondition(
    digestPattern.test(digest ?? "") &&
      /^[a-f0-9]{40}$/.test(sourceCommit ?? "") &&
      /^[1-9][0-9]{0,14}$/.test(runId ?? "") &&
      /^[1-9][0-9]{0,8}$/.test(runAttempt ?? ""),
    "Exact candidate digest, source, run and attempt are required.",
  );
  const { invoke, parse } = commands(run);
  requireCondition(
    /^gh version 2\.98\.0(?:\s|$)/.test(invoke("gh", ["--version"])),
    "Candidate provenance verification requires GitHub CLI 2.98.0.",
  );
  const results = parse("gh", [
    "attestation",
    "verify",
    files?.manifest ?? `oci://${imageRepository}@${digest}`,
    "--repo",
    repository,
    "--signer-workflow",
    `${repository}/${workflow}`,
    "--source-ref",
    "refs/heads/main",
    "--source-digest",
    sourceCommit,
    "--signer-digest",
    sourceCommit,
    "--deny-self-hosted-runners",
    "--cert-oidc-issuer",
    "https://token.actions.githubusercontent.com",
    "--predicate-type",
    "https://slsa.dev/provenance/v1",
    "--format",
    "json",
    ...(files ? ["--bundle", files.bundle, "--custom-trusted-root", files.trustedRoot] : []),
  ]);
  const invocation = `${repositoryURL}/actions/runs/${runId}/attempts/${runAttempt}`;
  const exact =
    Array.isArray(results) &&
    results.some(({ verificationResult: result }) => {
      const certificate = result?.signature?.certificate;
      const statement = result?.statement;
      return (
        certificate?.issuer === "https://token.actions.githubusercontent.com" &&
        certificate.subjectAlternativeName === signer &&
        certificate.buildSignerURI === signer &&
        certificate.buildSignerDigest === sourceCommit &&
        certificate.sourceRepositoryURI === repositoryURL &&
        certificate.sourceRepositoryIdentifier === "1302680759" &&
        certificate.sourceRepositoryOwnerIdentifier === "1063362" &&
        certificate.sourceRepositoryDigest === sourceCommit &&
        certificate.sourceRepositoryRef === "refs/heads/main" &&
        certificate.buildConfigURI === signer &&
        certificate.buildConfigDigest === sourceCommit &&
        certificate.runnerEnvironment === "github-hosted" &&
        certificate.buildTrigger === "workflow_dispatch" &&
        certificate.runInvocationURI === invocation &&
        Array.isArray(result.verifiedTimestamps) &&
        result.verifiedTimestamps.length > 0 &&
        statement?._type === "https://in-toto.io/Statement/v1" &&
        statement.predicateType === "https://slsa.dev/provenance/v1" &&
        statement.subject?.length === 1 &&
        statement.subject[0].name === imageRepository &&
        statement.subject[0].digest?.sha256 === digest.slice(7)
      );
    });
  requireCondition(exact, "No verified attestation matches the exact candidate publication.");

  return invocation;
}

export function verifyPublication(scope, run = execute) {
  const { digest, sourceCommit, runId, runAttempt, architecture } = scope;
  requireCondition(
    ["amd64", "arm64"].includes(architecture),
    "Exact candidate architecture is required.",
  );
  const invocation = verifyCandidateAttestation(scope, run);
  const { invoke, parse } = commands(run);

  // Signing happens before the job finishes: a valid signature can belong to a
  // failed publication. Read the immutable attempt, not the latest rerun state.
  const endpoint = `repos/${repository}/actions/runs/${runId}/attempts/${runAttempt}`;
  const attempt = parse("gh", ["api", endpoint]);
  requireCondition(
    String(attempt.id) === runId &&
      String(attempt.run_attempt) === runAttempt &&
      attempt.status === "completed" &&
      attempt.conclusion === "success" &&
      attempt.head_sha === sourceCommit &&
      attempt.head_branch === "main" &&
      attempt.path === workflow &&
      attempt.event === "workflow_dispatch" &&
      attempt.repository?.id === 1302680759 &&
      attempt.repository.full_name === repository &&
      attempt.head_repository?.id === 1302680759,
    "The exact attested workflow attempt did not complete successfully.",
  );
  const jobs = parse("gh", ["api", `${endpoint}/jobs?per_page=100`]);
  requireCondition(
    jobs.total_count === 2 &&
      jobs.jobs?.length === 2 &&
      ["native-sandbox", "publish-candidate"].every(
        (name) =>
          jobs.jobs.filter(
            (job) =>
              job.name === name &&
              job.status === "completed" &&
              job.conclusion === "success" &&
              String(job.run_id) === runId &&
              String(job.run_attempt) === runAttempt &&
              job.head_sha === sourceCommit,
          ).length === 1,
      ),
    "The attested build and publication jobs did not both complete successfully.",
  );
  invoke("docker", [
    "pull",
    "--quiet",
    "--platform",
    `linux/${architecture}`,
    `${imageRepository}@${digest}`,
  ]);
  const images = parse("docker", ["image", "inspect", `${imageRepository}@${digest}`]);
  const image = Array.isArray(images) && images.length === 1 ? images[0] : null;
  requireCondition(
    digestPattern.test(image?.Id ?? "") &&
      image.Os === "linux" &&
      image.Architecture === architecture &&
      image.Config?.User === "sandbox" &&
      image.RepoDigests?.includes(`${imageRepository}@${digest}`) &&
      image.Config.Labels?.["org.opencontainers.image.source"] === repositoryURL &&
      image.Config.Labels?.["dataground.dev.codex-compatibility-source"] === upstreamCommit &&
      image.Config.Labels?.["dataground.dev.certification-eligible"] === "false",
    "Pulled candidate image does not match the required architecture and isolation metadata.",
  );
  return {
    image: `${imageRepository}@${digest}`,
    imageId: image.Id,
    architecture,
    sourceCommit,
    invocation,
    certificationEligible: false,
  };
}

if (import.meta.main) {
  const [digest, sourceCommit, runId, runAttempt, architecture, ...extra] = process.argv.slice(2);
  try {
    requireCondition(extra.length === 0, "Unexpected candidate verification arguments.");
    console.log(
      JSON.stringify(verifyPublication({ digest, sourceCommit, runId, runAttempt, architecture })),
    );
  } catch (error) {
    console.error(error instanceof Error ? error.message : "Candidate verification failed.");
    process.exitCode = 1;
  }
}
