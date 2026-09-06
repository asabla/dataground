import { verifyOfflineAttestation } from "./check-codex-candidate-attestation.mjs";
import { verifyPublication } from "./check-codex-candidate-publication.mjs";

export function verifySupervisorPublication(scope, run) {
  return verifyPublication(scope, run, "supervisor");
}

export function verifySupervisorAttestation(scope, inputs, run) {
  return verifyOfflineAttestation(scope, inputs, run, "supervisor");
}

if (import.meta.main) {
  const [operation, digest, sourceCommit, runId, runAttempt, ...args] = process.argv.slice(2);
  try {
    let result;
    if (operation === "publication" && args.length === 1) {
      result = verifySupervisorPublication({
        digest,
        sourceCommit,
        runId,
        runAttempt,
        architecture: args[0],
      });
    } else if (operation === "attestation" && [4, 6].includes(args.length)) {
      const [manifest, bundle, trustedRoot, trustedRootSHA256, architecture, imageConfig] = args;
      result = verifySupervisorAttestation(
        { digest, sourceCommit, runId, runAttempt, architecture },
        { manifest, bundle, trustedRoot, trustedRootSHA256, imageConfig },
      );
    } else {
      throw new Error("Invalid supervisor verification arguments.");
    }
    console.log(JSON.stringify(result));
  } catch {
    console.error("Supervisor candidate verification failed; input and upstream details withheld.");
    process.exitCode = 1;
  }
}
