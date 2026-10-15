import { verifyOfflineAttestation } from "./check-codex-candidate-attestation.mjs";
import { verifyPublication } from "./check-codex-candidate-publication.mjs";

export function verifyWorkspacePublication(scope, run) {
  return verifyPublication(scope, run, "codex-workspace");
}

export function verifyWorkspaceAttestation(scope, inputs, run) {
  return verifyOfflineAttestation(scope, inputs, run, "codex-workspace");
}

if (import.meta.main) {
  const [operation, digest, sourceCommit, runId, runAttempt, ...args] = process.argv.slice(2);
  try {
    let result;
    if (operation === "publication" && args.length === 1) {
      result = verifyWorkspacePublication({
        digest,
        sourceCommit,
        runId,
        runAttempt,
        architecture: args[0],
      });
    } else if (operation === "attestation" && args.length === 6) {
      const [manifest, bundle, trustedRoot, trustedRootSHA256, architecture, imageConfig] = args;
      result = verifyWorkspaceAttestation(
        { digest, sourceCommit, runId, runAttempt, architecture },
        { manifest, bundle, trustedRoot, trustedRootSHA256, imageConfig },
      );
    } else {
      throw new Error("Invalid workspace verification arguments.");
    }
    console.log(JSON.stringify(result));
  } catch {
    console.error("Workspace candidate verification failed; input and upstream details withheld.");
    process.exitCode = 1;
  }
}
