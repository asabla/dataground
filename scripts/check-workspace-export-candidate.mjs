import { verifyOfflineAttestation } from "./check-codex-candidate-attestation.mjs";
import { verifyPublication } from "./check-codex-candidate-publication.mjs";

export function verifyWorkspaceExportPublication(scope, run) {
  return verifyPublication(scope, run, "codex-workspace-export");
}

export function verifyWorkspaceExportAttestation(scope, inputs, run) {
  return verifyOfflineAttestation(scope, inputs, run, "codex-workspace-export");
}

if (import.meta.main) {
  const [operation, digest, sourceCommit, runId, runAttempt, ...args] = process.argv.slice(2);
  try {
    let result;
    if (operation === "publication" && args.length === 1) {
      result = verifyWorkspaceExportPublication({
        digest,
        sourceCommit,
        runId,
        runAttempt,
        architecture: args[0],
      });
    } else if (operation === "attestation" && args.length === 6) {
      const [manifest, bundle, trustedRoot, trustedRootSHA256, architecture, imageConfig] = args;
      result = verifyWorkspaceExportAttestation(
        { digest, sourceCommit, runId, runAttempt, architecture },
        { manifest, bundle, trustedRoot, trustedRootSHA256, imageConfig },
      );
    } else {
      throw new Error("Invalid workspace export verification arguments.");
    }
    console.log(JSON.stringify(result));
  } catch {
    console.error(
      "Workspace export candidate verification failed; input and upstream details withheld.",
    );
    process.exitCode = 1;
  }
}
