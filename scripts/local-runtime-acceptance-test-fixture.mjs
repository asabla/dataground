import assert from "node:assert/strict";
import { createHash, generateKeyPairSync, randomBytes, sign } from "node:crypto";
import { lstatSync, mkdirSync, readFileSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { dirname, isAbsolute, join, normalize } from "node:path";
import { readPrivateSnapshot } from "./check-codex-candidate-attestation.mjs";
import { canonicalJSON, prepareAcceptance, verifyAcceptance } from "./local-runtime-acceptance.mjs";

const hash = (bytes) => createHash("sha256").update(bytes).digest("hex");
const id = (prefix) => `${prefix}_${randomBytes(12).toString("hex")}`;

// Only test callers use this helper. The key stays in memory, resource IDs are
// newly generated, and the signer never represents an operational reviewer.
export function createTestAcceptance(inputFile) {
  const input = JSON.parse(readPrivateSnapshot(inputFile, 64 << 10));
  assert.ok(
    [
      "dataground.test.local-acceptance-input/v1",
      "dataground.test.local-acceptance-input/v2",
    ].includes(input.contract),
  );
  const owned = input.contract === "dataground.test.local-acceptance-input/v2";
  const version = owned ? 4 : 3;
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
  const profile = owned
    ? "openshell-codex-workspace-export-candidate-development/v1"
    : "openshell-codex-workspace-candidate-development/v1";
  const trust = canonicalJSON({
    contract: `dataground.local-runtime-acceptance-trust/v${version}`,
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
    contract: `dataground.local-runtime-acceptance-statement/v${version}`,
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
    ...(owned
      ? {
          nativeOwnerContract: "dataground.native-owner/v1",
          workspaceChangesContract: "dataground.workspace-changes/v1",
        }
      : {}),
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
      Buffer.from(`DataGround local candidate runtime acceptance v${version}\n`),
      canonicalJSON(statement),
    ]),
  );
  const envelope = canonicalJSON({
    contract: `dataground.local-runtime-acceptance-envelope/v${version}`,
    statement,
    signature: {
      keyId: "ephemeral-conformance-key",
      value: sign(null, message, privateKey).toString("base64"),
    },
  });
  expected.envelopeSHA256 = hash(envelope);
  const accepted = verifyAcceptance(envelope, trust, artifacts, expected);
  return {
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
    version,
  };
}

if (import.meta.main) {
  let created;
  try {
    const [inputFile, destination, ...extra] = process.argv.slice(2);
    if (extra.length || !isAbsolute(destination ?? "") || normalize(destination) !== destination)
      throw new Error("A new absolute test fixture directory is required.");
    const parent = dirname(destination);
    const metadata = lstatSync(parent);
    if (
      !metadata.isDirectory() ||
      metadata.uid !== process.getuid() ||
      (metadata.mode & 0o777) !== 0o700 ||
      realpathSync(parent) !== parent
    )
      throw new Error("The test fixture parent must be private and owned.");
    const f = createTestAcceptance(inputFile);
    mkdirSync(destination, { mode: 0o700 });
    created = destination;
    // Write only this explicit public-material allowlist, never the key object.
    const values = {
      "envelope.json": f.envelope,
      "trust.json": f.trust,
      ...Object.fromEntries(
        Object.entries(f.files).map(([key, [name]]) => [name, f.artifacts[key]]),
      ),
      "fixture.json": canonicalJSON({
        contract: `dataground.test.local-acceptance-fixture/v${f.version === 4 ? 2 : 1}`,
        ...f.accepted,
        envelopeSHA256: hash(f.envelope),
        trustSHA256: hash(f.trust),
        sourceRevision: f.expected.sourceRevision,
        testTrust: true,
      }),
    };
    for (const [name, bytes] of Object.entries(values))
      writeFileSync(join(destination, name), bytes, { mode: 0o600, flag: "wx" });
    console.log("Ephemeral test fixture prepared; it is not operational or release acceptance.");
  } catch {
    if (created) rmSync(created, { recursive: true, force: true });
    console.error(
      "Test acceptance fixture preparation failed; input and upstream details withheld.",
    );
    process.exitCode = 1;
  }
}
