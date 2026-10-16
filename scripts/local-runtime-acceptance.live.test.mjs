import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash, randomBytes, sign } from "node:crypto";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { canonicalJSON, verifyAcceptance } from "./local-runtime-acceptance.mjs";

import { createTestAcceptance } from "./local-runtime-acceptance-test-fixture.mjs";

const inputFile = process.env.DATAGROUND_TEST_LOCAL_ACCEPTANCE_INPUT;
const hash = (bytes) => createHash("sha256").update(bytes).digest("hex");
const id = (prefix) => `${prefix}_${randomBytes(12).toString("hex")}`;

test("published source candidate acceptance with ephemeral test trust", {
  skip: !inputFile,
}, () => {
  const {
    artifacts,
    files,
    statement,
    trust,
    envelope,
    expected,
    accepted,
    privateKey,
    profile,
    scope,
    expires,
  } = createTestAcceptance(inputFile);
  assert.equal(accepted.profile, profile);
  assert.equal(accepted.certificationEligible, false);
  assert.equal(accepted.workspaceSourceContract, statement.workspaceSourceContract);
  assert.deepEqual(accepted.scope, scope);

  for (const key of Object.keys(scope)) {
    assert.throws(
      () =>
        verifyAcceptance(envelope, trust, artifacts, {
          ...expected,
          scope: {
            ...scope,
            [key]: id(key === "isolationDomainId" ? "iso" : key === "serviceId" ? "svc" : "rev"),
          },
        }),
      /target, digest, generation or revocation/,
    );
  }
  for (const change of [
    { minimumGeneration: 2 },
    { rejectedAcceptanceIds: new Set([statement.acceptanceId]) },
    { envelopeSHA256: "0".repeat(64) },
  ]) {
    assert.throws(
      () => verifyAcceptance(envelope, trust, artifacts, { ...expected, ...change }),
      /target, digest, generation or revocation/,
    );
  }
  assert.throws(
    () => verifyAcceptance(envelope, trust, artifacts, { ...expected, now: expires }),
    /expired/,
  );
  assert.throws(
    () =>
      verifyAcceptance(envelope, trust, artifacts, {
        ...expected,
        trustProfileSHA256: "0".repeat(64),
      }),
    /trust, source, scope or profile/,
  );
  const changed = JSON.parse(envelope);
  changed.statement.reason = "Changed after signing.";
  const tampered = canonicalJSON(changed);
  assert.throws(
    () =>
      verifyAcceptance(tampered, trust, artifacts, { ...expected, envelopeSHA256: hash(tampered) }),
    /signature is invalid/,
  );

  for (const key of ["bundle", "supervisorBundle"]) {
    const invalid = Buffer.from("{}\n");
    const resigned = JSON.parse(envelope);
    const subject = key === "bundle" ? resigned.statement : resigned.statement.supervisor;
    subject.bundleSHA256 = hash(invalid);
    resigned.signature.value = sign(
      null,
      Buffer.concat([
        Buffer.from("DataGround local candidate runtime acceptance v3\n"),
        canonicalJSON(resigned.statement),
      ]),
      privateKey,
    ).toString("base64");
    const forged = canonicalJSON(resigned);
    assert.throws(
      () =>
        verifyAcceptance(
          forged,
          trust,
          { ...artifacts, [key]: invalid },
          {
            ...expected,
            envelopeSHA256: hash(forged),
          },
        ),
      /verification operation/,
    );
  }

  // Exercise the worker-facing CLI over private snapshots as well as the
  // in-process verifier. Only public fixture material reaches temporary disk.
  const directory = mkdtempSync(join(tmpdir(), "dataground-acceptance-test-"));
  try {
    const envelopeFile = join(directory, "envelope.json");
    const trustFile = join(directory, "trust.json");
    writeFileSync(envelopeFile, envelope, { mode: 0o600, flag: "wx" });
    writeFileSync(trustFile, trust, { mode: 0o600, flag: "wx" });
    for (const [key, [name]] of Object.entries(files)) {
      writeFileSync(join(directory, name), artifacts[key], { mode: 0o600, flag: "wx" });
    }
    const result = execFileSync(
      process.execPath,
      [
        fileURLToPath(new URL("./local-runtime-acceptance.mjs", import.meta.url)),
        "verify",
        envelopeFile,
        trustFile,
        directory,
        expected.trustProfileSHA256,
        expected.sourceRevision,
        expected.envelopeSHA256,
        scope.isolationDomainId,
        scope.serviceId,
        scope.revisionId,
        "1",
      ],
      { encoding: "utf8", timeout: 75000, maxBuffer: 64 << 10, stdio: ["ignore", "pipe", "pipe"] },
    );
    assert.deepEqual(JSON.parse(result), accepted);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});
