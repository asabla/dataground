import assert from "node:assert/strict";
import { createHash, generateKeyPairSync, sign } from "node:crypto";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { fixture, imageRepository, scope } from "./codex-candidate-fixture.mjs";
import { canonicalJSON, prepareAcceptance, verifyAcceptance } from "./local-runtime-acceptance.mjs";

const hash = (bytes) => createHash("sha256").update(bytes).digest("hex");
const profile = readFileSync(
  new URL("../deploy/openshell/development-profile.json", import.meta.url),
);
const record = JSON.parse(
  readFileSync(
    new URL(
      "../deploy/openshell/diagnostics/codex-published-candidate-arm64-20260906.json",
      import.meta.url,
    ),
  ),
);

function inputs() {
  const f = fixture();
  const { publicKey, privateKey } = generateKeyPairSync("ed25519");
  const target = {
    isolationDomainId: "iso_0123456789abcdefghij",
    serviceId: "svc_0123456789abcdefghij",
    revisionId: "rev_0123456789abcdefghij",
  };
  const trust = {
    contract: "dataground.local-runtime-acceptance-trust/v1",
    scope: target,
    reviewerId: "reviewer_local",
    keyId: "key_local",
    publicKey: publicKey.export({ format: "der", type: "spki" }).subarray(-32).toString("base64"),
    trustedRootSHA256: hash("synthetic trust root"),
    notBefore: "2026-09-06T00:00:00.000Z",
    notAfter: "2026-09-08T00:00:00.000Z",
  };
  const trustBytes = canonicalJSON(trust);
  const config = Buffer.from(
    JSON.stringify({ architecture: "arm64", os: "linux", config: f.image.Config }),
  );
  const configSHA256 = hash(config);
  const manifest = Buffer.from(
    JSON.stringify({
      schemaVersion: 2,
      mediaType: "application/vnd.docker.distribution.manifest.v2+json",
      config: {
        mediaType: "application/vnd.docker.container.image.v1+json",
        size: config.length,
        digest: `sha256:${configSHA256}`,
      },
    }),
  );
  const diagnostic = structuredClone(record);
  diagnostic.profile.sandboxImage = `sha256:${configSHA256}`;
  const artifacts = {
    diagnostic: Buffer.from(JSON.stringify(diagnostic)),
    imageConfig: config,
    manifest,
    bundle: Buffer.from("synthetic signature bundle"),
    trustedRoot: Buffer.from("synthetic trust root"),
  };
  const publication = { ...scope, digest: `sha256:${hash(manifest)}` };
  f.result.statement.subject[0].digest.sha256 = hash(manifest);
  f.image.Id = diagnostic.profile.sandboxImage;
  f.image.RepoDigests = [`${imageRepository}@${publication.digest}`];
  const statement = {
    contract: "dataground.local-runtime-acceptance-statement/v1",
    acceptanceId: "rtlocal_0123456789abcdefghij",
    generation: 3,
    scope: target,
    profile: "openshell-codex-candidate-development/v1",
    sourceRevision: diagnostic.run.sourceCommit,
    publication,
    model: diagnostic.run.model,
    localImageId: diagnostic.profile.sandboxImage,
    profileSHA256: hash(profile),
    diagnosticSHA256: hash(artifacts.diagnostic),
    configSHA256,
    bundleSHA256: hash(artifacts.bundle),
    trustProfileSHA256: hash(trustBytes),
    issuedAt: "2026-09-06T11:00:00.000Z",
    expiresAt: "2026-09-07T10:00:00.000Z",
    reviewerId: "reviewer_local",
    reason: "Synthetic local acceptance test.",
    publicationCompletionChecked: true,
    certificationEligible: false,
    deploymentScope: "loopback-development-only",
  };
  const expected = {
    trustProfileSHA256: hash(trustBytes),
    sourceRevision: statement.sourceRevision,
    scope: target,
    minimumGeneration: 3,
    rejectedAcceptanceIds: new Set(),
    now: Date.parse("2026-09-06T12:00:00.000Z"),
  };
  const envelopeFor = (value = statement, key = privateKey) =>
    canonicalJSON({
      contract: "dataground.local-runtime-acceptance-envelope/v1",
      statement: value,
      signature: {
        keyId: trust.keyId,
        value: sign(
          null,
          Buffer.concat([
            Buffer.from("DataGround local candidate runtime acceptance v1\n"),
            canonicalJSON(value),
          ]),
          key,
        ).toString("base64"),
      },
    });
  return { f, statement, trust, trustBytes, artifacts, expected, envelopeFor, privateKey };
}

test("preparation checks completed publication and offline acceptance verifies the signature", () => {
  const { f, statement, trustBytes, artifacts, expected, envelopeFor } = inputs();
  const message = prepareAcceptance(
    canonicalJSON(statement),
    trustBytes,
    artifacts,
    expected,
    f.run,
  );
  assert.deepEqual(
    message,
    Buffer.concat([
      Buffer.from("DataGround local candidate runtime acceptance v1\n"),
      canonicalJSON(statement),
    ]),
  );
  assert.equal(
    f.calls.filter(([command, args]) => command === "docker" && args[0] === "pull").length,
    1,
  );
  f.calls.length = 0;
  const envelope = envelopeFor();
  const result = verifyAcceptance(
    envelope,
    trustBytes,
    artifacts,
    { ...expected, envelopeSHA256: hash(envelope) },
    f.run,
  );
  assert.equal(result.model, statement.model);
  assert.equal(result.image, `${imageRepository}@${statement.publication.digest}`);
  assert.equal(result.certificationEligible, false);
  assert.equal(result.deploymentScope, "loopback-development-only");
  assert.equal(f.calls.length, 2);
  assert.ok(
    f.calls.every(
      ([command, args]) => command === "gh" && ["--version", "attestation"].includes(args[0]),
    ),
  );
});

test("a valid image signature cannot prepare a failed publication or a different local image", () => {
  for (const mutate of [
    (f) => {
      f.attempt.conclusion = "failure";
    },
    (f) => {
      f.jobs.jobs[1].conclusion = "failure";
    },
    (f) => {
      f.image.Id = `sha256:${"a".repeat(64)}`;
    },
  ]) {
    const { f, statement, trustBytes, artifacts, expected } = inputs();
    mutate(f);
    assert.throws(() =>
      prepareAcceptance(canonicalJSON(statement), trustBytes, artifacts, expected, f.run),
    );
  }
});

test("acceptance rejects wrong scope, generation, revocation, independent trust and expiry", () => {
  const mutations = [
    (e) => {
      e.scope = { ...e.scope, isolationDomainId: "iso_abcdefghij0123456789" };
    },
    (e) => {
      e.scope = { ...e.scope, serviceId: "svc_abcdefghij0123456789" };
    },
    (e) => {
      e.scope = { ...e.scope, revisionId: "rev_abcdefghij0123456789" };
    },
    (e) => {
      e.minimumGeneration = 4;
    },
    (e) => {
      e.minimumGeneration = 0;
    },
    (e) => {
      e.minimumGeneration = Number.NaN;
    },
    (e) => {
      e.rejectedAcceptanceIds.add("rtlocal_0123456789abcdefghij");
    },
    (e) => {
      delete e.rejectedAcceptanceIds;
    },
    (e) => {
      e.trustProfileSHA256 = "0".repeat(64);
    },
    (e) => {
      e.sourceRevision = "0".repeat(40);
    },
    (e) => {
      e.now = Date.parse("2026-09-07T10:00:00.000Z");
    },
    (e) => {
      e.now = Date.parse("2026-09-06T10:00:00.000Z");
    },
    (e) => {
      e.now = Number.NaN;
    },
    (e) => {
      e.envelopeSHA256 = "0".repeat(64);
    },
  ];
  for (const mutate of mutations) {
    const { f, trustBytes, artifacts, expected, envelopeFor } = inputs();
    const envelope = envelopeFor();
    expected.envelopeSHA256 = hash(envelope);
    mutate(expected);
    assert.throws(() => verifyAcceptance(envelope, trustBytes, artifacts, expected, f.run));
    assert.equal(f.calls.length, 0);
  }
});

test("signed statements cannot relax profile, model, evidence age or trust bindings", () => {
  const mutations = [
    (s) => {
      s.certificationEligible = true;
    },
    (s) => {
      s.publicationCompletionChecked = false;
    },
    (s) => {
      s.deploymentScope = "production";
    },
    (s) => {
      s.profile = "openshell-codex-development/v1";
    },
    (s) => {
      s.profileSHA256 = "0".repeat(64);
    },
    (s) => {
      s.model = "different-model";
    },
    (s) => {
      s.localImageId = `sha256:${"0".repeat(64)}`;
    },
    (s) => {
      s.reviewerId = "another_reviewer";
    },
    (s) => {
      s.trustProfileSHA256 = "0".repeat(64);
    },
    (s) => {
      s.expiresAt = "2026-09-07T10:30:00.000Z";
    },
    (s) => {
      s.expiresAt = "2026-09-07T12:30:00.000Z";
    },
    (s) => {
      s.issuedAt = "2026-09-06T10:14:00.000Z";
    },
    (s) => {
      s.expiresAt = "2026-09-07T10:00:00Z";
    },
    (s) => {
      s.extra = true;
    },
  ];
  for (const mutate of mutations) {
    const { f, statement, trustBytes, artifacts, expected, envelopeFor } = inputs();
    mutate(statement);
    const envelope = envelopeFor();
    assert.throws(() =>
      verifyAcceptance(
        envelope,
        trustBytes,
        artifacts,
        { ...expected, envelopeSHA256: hash(envelope) },
        f.run,
      ),
    );
    assert.equal(f.calls.length, 0);
  }
});

test("tampered artifacts, unsigned changes, wrong keys and noncanonical documents fail", () => {
  for (const artifact of ["diagnostic", "manifest", "imageConfig", "bundle", "trustedRoot"]) {
    const { f, trustBytes, artifacts, expected, envelopeFor } = inputs();
    const envelope = envelopeFor();
    artifacts[artifact] = Buffer.concat([artifacts[artifact], Buffer.from(" ")]);
    assert.throws(() =>
      verifyAcceptance(
        envelope,
        trustBytes,
        artifacts,
        { ...expected, envelopeSHA256: hash(envelope) },
        f.run,
      ),
    );
    assert.equal(f.calls.length, 0);
  }
  for (const mutate of [
    (bytes) => Buffer.concat([bytes, Buffer.from(" ")]),
    (bytes) => {
      const e = JSON.parse(bytes);
      e.statement.reason = "Unsigned change";
      return canonicalJSON(e);
    },
    (bytes) => {
      const e = JSON.parse(bytes);
      e.signature.keyId = "other_key";
      return canonicalJSON(e);
    },
    (bytes) => {
      const e = JSON.parse(bytes);
      e.signature.value = Buffer.alloc(64).toString("base64");
      return canonicalJSON(e);
    },
    (bytes) =>
      Buffer.from(bytes.toString().replace('"contract":', '"contract":"duplicate","contract":')),
  ]) {
    const { f, trustBytes, artifacts, expected, envelopeFor } = inputs();
    const envelope = mutate(envelopeFor());
    assert.throws(() =>
      verifyAcceptance(
        envelope,
        trustBytes,
        artifacts,
        { ...expected, envelopeSHA256: hash(envelope) },
        f.run,
      ),
    );
    assert.equal(f.calls.length, 0);
  }
  const { f, statement, trustBytes, artifacts, expected, envelopeFor } = inputs();
  const envelope = envelopeFor(statement, generateKeyPairSync("ed25519").privateKey);
  assert.throws(() =>
    verifyAcceptance(
      envelope,
      trustBytes,
      artifacts,
      { ...expected, envelopeSHA256: hash(envelope) },
      f.run,
    ),
  );
  assert.equal(f.calls.length, 0);
});

test("a valid local signature still requires independent image provenance verification", () => {
  const { f, trustBytes, artifacts, expected, envelopeFor } = inputs();
  const envelope = envelopeFor();
  f.certificate.sourceRepositoryDigest = "0".repeat(40);
  assert.throws(() =>
    verifyAcceptance(
      envelope,
      trustBytes,
      artifacts,
      { ...expected, envelopeSHA256: hash(envelope) },
      f.run,
    ),
  );
  assert.equal(f.calls.length, 2);
});

test("expiry during external verification cannot release a signing message or acceptance", () => {
  for (const prepare of [false, true]) {
    const { f, statement, trustBytes, artifacts, expected, envelopeFor } = inputs();
    const envelope = envelopeFor();
    expected.envelopeSHA256 = hash(envelope);
    const run = (command, args) => {
      expected.now = Date.parse(statement.expiresAt);
      return f.run(command, args);
    };
    assert.throws(() =>
      prepare
        ? prepareAcceptance(canonicalJSON(statement), trustBytes, artifacts, expected, run)
        : verifyAcceptance(envelope, trustBytes, artifacts, expected, run),
    );
  }
});

test("a signed replacement diagnostic still has to pass every local case and cleanup check", () => {
  for (const mutate of [
    (d) => {
      d.schemaVersion = "dataground.dev.openshell-runtime-diagnostic/v4";
      d.profile.runtimePolicySHA256 =
        "a1d56c0470c3264c4c37183352d783ebb67911d92ef2eb6ec5f7c76c61f69f39";
      d.policySource = {
        profile: "rosetta-development/v1",
        compilerSourceCommit: "320158f1e4a4eea378d82c1527f4a7af5fb9855b",
        inputSHA256: "b2895b9172c50ba7a5fdf574cebdf6789258cc8ce9f90ce5ad8f2b1ff0a825ab",
      };
    },
    (d) => {
      d.checks.pop();
    },
    (d) => {
      d.checks[0].result = "failed";
    },
    (d) => {
      d.candidateCredentialCheck = "failed";
    },
    (d) => {
      d.run.origin = "ci";
    },
    (d) => {
      d.run.sourceCommit = "0".repeat(40);
    },
    (d) => {
      d.profile.runtimePolicySHA256 = "0".repeat(64);
    },
    (d) => {
      d.cleanup.sandbox.name = "other-sandbox";
    },
    (d) => {
      d.checks[1].observationCommitment = d.checks[0].observationCommitment;
    },
  ]) {
    const { f, statement, trustBytes, artifacts, expected, envelopeFor } = inputs();
    const diagnostic = JSON.parse(artifacts.diagnostic);
    mutate(diagnostic);
    artifacts.diagnostic = Buffer.from(JSON.stringify(diagnostic));
    statement.diagnosticSHA256 = hash(artifacts.diagnostic);
    const envelope = envelopeFor();
    assert.throws(() =>
      verifyAcceptance(
        envelope,
        trustBytes,
        artifacts,
        { ...expected, envelopeSHA256: hash(envelope) },
        f.run,
      ),
    );
    assert.equal(f.calls.length, 0);
  }
});

test("independently selected signing trust must cover the exact scope and validity", () => {
  for (const mutate of [
    (trust) => {
      trust.scope = { ...trust.scope, revisionId: "rev_abcdefghij0123456789" };
    },
    (trust) => {
      trust.notBefore = "2026-09-06T11:01:00.000Z";
    },
    (trust) => {
      trust.notAfter = "2026-09-07T09:59:59.999Z";
    },
    (trust) => {
      trust.reviewerId = "other_reviewer";
    },
    (trust) => {
      trust.publicKey = Buffer.alloc(31).toString("base64");
    },
    (trust) => {
      trust.trustedRootSHA256 = "0".repeat(64);
    },
    (trust) => {
      trust.extra = true;
    },
  ]) {
    const { f, trust, statement, artifacts, expected, envelopeFor } = inputs();
    mutate(trust);
    const trustBytes = canonicalJSON(trust);
    expected.trustProfileSHA256 = hash(trustBytes);
    statement.trustProfileSHA256 = hash(trustBytes);
    const envelope = envelopeFor();
    assert.throws(() =>
      verifyAcceptance(
        envelope,
        trustBytes,
        artifacts,
        { ...expected, envelopeSHA256: hash(envelope) },
        f.run,
      ),
    );
    assert.equal(f.calls.length, 0);
  }
});
