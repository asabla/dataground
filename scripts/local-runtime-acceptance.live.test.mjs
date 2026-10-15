import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash, generateKeyPairSync, randomBytes, sign } from "node:crypto";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { isAbsolute, join, normalize } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { readPrivateSnapshot } from "./check-codex-candidate-attestation.mjs";
import { canonicalJSON, prepareAcceptance, verifyAcceptance } from "./local-runtime-acceptance.mjs";

const inputFile = process.env.DATAGROUND_TEST_LOCAL_ACCEPTANCE_INPUT;
const hash = (bytes) => createHash("sha256").update(bytes).digest("hex");
const id = (prefix) => `${prefix}_${randomBytes(12).toString("hex")}`;

test("published source candidate acceptance with ephemeral test trust", {
  skip: !inputFile,
}, () => {
  const input = JSON.parse(readPrivateSnapshot(inputFile, 64 << 10));
  assert.equal(input.contract, "dataground.test.local-acceptance-input/v1");
  assert.deepEqual(Object.keys(input).sort(), [
    "contract",
    "diagnosticSHA256",
    "evidenceDirectory",
    "model",
    "publication",
    "sourceRevision",
    "supervisorPublication",
    "trustedRootSHA256",
  ]);
  assert.ok(isAbsolute(input.evidenceDirectory));
  assert.equal(normalize(input.evidenceDirectory), input.evidenceDirectory);
  const files = {
    diagnostic: ["diagnostic.json", 64 << 10],
    manifest: ["manifest.json", 1 << 20],
    imageConfig: ["image-config.json", 1 << 20],
    bundle: ["bundle.jsonl", 4 << 20],
    trustedRoot: ["trusted-root.jsonl", 1 << 20],
    supervisorManifest: ["supervisor-manifest.json", 1 << 20],
    supervisorImageConfig: ["supervisor-image-config.json", 1 << 20],
    supervisorBundle: ["supervisor-bundle.jsonl", 4 << 20],
  };
  const artifacts = Object.fromEntries(
    Object.entries(files).map(([key, [name, maximum]]) => [
      key,
      readPrivateSnapshot(join(input.evidenceDirectory, name), maximum),
    ]),
  );
  assert.equal(hash(artifacts.diagnostic), input.diagnosticSHA256);
  assert.equal(hash(artifacts.trustedRoot), input.trustedRootSHA256);
  const diagnostic = JSON.parse(artifacts.diagnostic);
  const now = Date.now();
  const expires = Math.min(now + 30 * 60 * 1000, Date.parse(diagnostic.run.finishedAt) + 86400000);
  assert.ok(expires > now + 5 * 60 * 1000, "fresh diagnostic evidence is required");
  const scope = { isolationDomainId: id("iso"), serviceId: id("svc"), revisionId: id("rev") };
  // This key exists only inside the test process. No operational reviewer or
  // independently administered trust is implied by this conformance fixture.
  const { publicKey, privateKey } = generateKeyPairSync("ed25519");
  const profile = "openshell-codex-workspace-candidate-development/v1";
  const trust = canonicalJSON({
    contract: "dataground.local-runtime-acceptance-trust/v3",
    profile,
    scope,
    reviewerId: "ephemeral-conformance-test",
    keyId: "ephemeral-conformance-key",
    publicKey: publicKey.export({ format: "der", type: "spki" }).subarray(-32).toString("base64"),
    trustedRootSHA256: input.trustedRootSHA256,
    notBefore: new Date(now).toISOString(),
    notAfter: new Date(expires).toISOString(),
  });
  const statement = {
    contract: "dataground.local-runtime-acceptance-statement/v3",
    acceptanceId: id("rtlocal"),
    generation: 1,
    scope,
    profile,
    sourceRevision: input.sourceRevision,
    publication: input.publication,
    model: input.model,
    localImageId: diagnostic.profile.sandboxImage,
    profileSHA256: hash(
      readFileSync(new URL("../deploy/openshell/development-profile.json", import.meta.url)),
    ),
    diagnosticSHA256: input.diagnosticSHA256,
    configSHA256: hash(artifacts.imageConfig),
    bundleSHA256: hash(artifacts.bundle),
    trustProfileSHA256: hash(trust),
    issuedAt: new Date(now).toISOString(),
    expiresAt: new Date(expires).toISOString(),
    reviewerId: "ephemeral-conformance-test",
    reason: "Ephemeral conformance test only; not operational acceptance or release evidence.",
    publicationCompletionChecked: true,
    certificationEligible: false,
    deploymentScope: "loopback-development-only",
    workspaceSourceContract: "dataground.workspace-source-materialization/v1",
    supervisor: {
      publication: input.supervisorPublication,
      localImageId: diagnostic.profile.supervisorImage,
      configSHA256: hash(artifacts.supervisorImageConfig),
      bundleSHA256: hash(artifacts.supervisorBundle),
    },
  };
  const expected = {
    trustProfileSHA256: hash(trust),
    sourceRevision: input.sourceRevision,
    scope,
    minimumGeneration: 1,
    rejectedAcceptanceIds: new Set(),
  };
  // Use the real verifier, completed GitHub workflows, image pulls, and exact
  // local identities. No command result or signature verification is mocked.
  const message = prepareAcceptance(canonicalJSON(statement), trust, artifacts, expected);
  assert.deepEqual(
    message,
    Buffer.concat([
      Buffer.from("DataGround local candidate runtime acceptance v3\n"),
      canonicalJSON(statement),
    ]),
  );
  const envelope = canonicalJSON({
    contract: "dataground.local-runtime-acceptance-envelope/v3",
    statement,
    signature: {
      keyId: "ephemeral-conformance-key",
      value: sign(null, message, privateKey).toString("base64"),
    },
  });
  expected.envelopeSHA256 = hash(envelope);
  const accepted = verifyAcceptance(envelope, trust, artifacts, expected);
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
