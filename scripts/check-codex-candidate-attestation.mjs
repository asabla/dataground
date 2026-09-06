import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  closeSync,
  constants,
  fstatSync,
  mkdtempSync,
  openSync,
  readSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { isAbsolute, join, normalize } from "node:path";
import { verifyCandidateAttestation } from "./check-codex-candidate-publication.mjs";

function sha256(bytes) {
  return createHash("sha256").update(bytes).digest("hex");
}

function readPrivateSnapshot(file, maximumBytes) {
  if (!isAbsolute(file) || normalize(file) !== file || typeof process.getuid !== "function") {
    throw new Error("Attestation inputs require clean absolute private file paths.");
  }
  const fd = openSync(file, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
  try {
    const before = fstatSync(fd, { bigint: true });
    if (
      !before.isFile() ||
      before.uid !== BigInt(process.getuid()) ||
      (before.mode & 0o077n) !== 0n ||
      before.size <= 0n ||
      before.size > BigInt(maximumBytes)
    ) {
      throw new Error("Attestation input metadata is invalid.");
    }
    const buffer = Buffer.alloc(maximumBytes + 1);
    let length = 0;
    while (length < buffer.length) {
      const count = readSync(fd, buffer, length, buffer.length - length, null);
      if (count === 0) break;
      length += count;
    }
    const bytes = buffer.subarray(0, length);
    const after = fstatSync(fd, { bigint: true });
    if (
      bytes.length > maximumBytes ||
      BigInt(bytes.length) !== after.size ||
      ["dev", "ino", "size", "mtimeNs", "ctimeNs"].some((key) => before[key] !== after[key])
    ) {
      throw new Error("Attestation input changed during acquisition.");
    }
    return bytes;
  } finally {
    closeSync(fd);
  }
}

export function verifyOfflineAttestation(scope, inputs, run = execFileSync) {
  if (!/^[a-f0-9]{64}$/.test(inputs.trustedRootSHA256 ?? "")) {
    throw new Error("An independently pinned trusted-root digest is required.");
  }
  const manifest = readPrivateSnapshot(inputs.manifest, 1 << 20);
  const bundle = readPrivateSnapshot(inputs.bundle, 4 << 20);
  const trustedRoot = readPrivateSnapshot(inputs.trustedRoot, 1 << 20);
  if (
    `sha256:${sha256(manifest)}` !== scope.digest ||
    sha256(trustedRoot) !== inputs.trustedRootSHA256
  ) {
    throw new Error("Manifest or trusted-root digest does not match.");
  }
  const directory = mkdtempSync(join(tmpdir(), "dataground-candidate-attestation-"));
  try {
    const files = {
      manifest: join(directory, "manifest.json"),
      bundle: join(directory, "bundle.jsonl"),
      trustedRoot: join(directory, "trusted-root.jsonl"),
    };
    for (const [key, bytes] of Object.entries({ manifest, bundle, trustedRoot })) {
      writeFileSync(files[key], bytes, { mode: 0o600, flag: "wx" });
    }
    const invocation = verifyCandidateAttestation(
      scope,
      (_command, args) =>
        run("gh", args, {
          encoding: "utf8",
          timeout: 60000,
          maxBuffer: 8 << 20,
          stdio: ["ignore", "pipe", "pipe"],
          cwd: directory,
          env: {
            PATH: process.env.PATH ?? "/usr/bin:/bin",
            GH_HOST: "github.com",
            GH_PROMPT_DISABLED: "1",
            GH_CONFIG_DIR: join(directory, "gh-config"),
            XDG_STATE_HOME: join(directory, "gh-state"),
            // Bundle and trusted-root inputs make this an offline operation. Any
            // unexpected HTTP request must fail instead of using ambient access.
            HTTPS_PROXY: "http://127.0.0.1:9",
            HTTP_PROXY: "http://127.0.0.1:9",
          },
        }),
      files,
    );
    return {
      imageDigest: scope.digest,
      sourceCommit: scope.sourceCommit,
      invocation,
      bundleSHA256: sha256(bundle),
      trustedRootSHA256: inputs.trustedRootSHA256,
      publicationCompletionChecked: false,
      certificationEligible: false,
    };
  } finally {
    rmSync(directory, { recursive: true });
  }
}

if (import.meta.main) {
  const [
    digest,
    sourceCommit,
    runId,
    runAttempt,
    manifest,
    bundle,
    trustedRoot,
    trustedRootSHA256,
    ...extra
  ] = process.argv.slice(2);
  try {
    if (extra.length || !trustedRootSHA256) throw new Error("Invalid arguments.");
    console.log(
      JSON.stringify(
        verifyOfflineAttestation(
          { digest, sourceCommit, runId, runAttempt },
          { manifest, bundle, trustedRoot, trustedRootSHA256 },
        ),
      ),
    );
  } catch {
    console.error(
      "Offline candidate attestation verification failed; input and upstream details withheld.",
    );
    process.exitCode = 1;
  }
}
