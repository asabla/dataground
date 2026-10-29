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
  // Synthetic test input uses the current topology; archived evidence stays unchanged.
  const diagnostic = structuredClone(record);
  diagnostic.profile.composeSHA256 = JSON.parse(profile).runtime.conformance.topology.composeSHA256;
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
      contract: `dataground.local-runtime-acceptance-envelope/v${value.contract.endsWith("/v4") ? 4 : value.contract.endsWith("/v3") ? 3 : value.contract.endsWith("/v2") ? 2 : 1}`,
      statement: value,
      signature: {
        keyId: trust.keyId,
        value: sign(
          null,
          Buffer.concat([
            Buffer.from(
              `DataGround local candidate runtime acceptance v${value.contract.endsWith("/v4") ? 4 : value.contract.endsWith("/v3") ? 3 : value.contract.endsWith("/v2") ? 2 : 1}\n`,
            ),
            canonicalJSON(value),
          ]),
          key,
        ).toString("base64"),
      },
    });
  return { f, statement, trust, trustBytes, artifacts, expected, envelopeFor, privateKey };
}

function strictInputs() {
  const base = inputs();
  const supervisor = fixture();
  const repository = "ghcr.io/asabla/dataground-supervisor-candidate";
  const workflow = ".github/workflows/openshell-supervisor-compatibility.yml";
  const signer = `https://github.com/asabla/dataground/${workflow}@refs/heads/main`;
  for (const key of ["subjectAlternativeName", "buildSignerURI", "buildConfigURI"])
    supervisor.certificate[key] = signer;
  supervisor.certificate.runInvocationURI =
    "https://github.com/asabla/dataground/actions/runs/43/attempts/1";
  supervisor.attempt.id = 43;
  supervisor.attempt.path = workflow;
  supervisor.jobs.jobs[0].name = "strict-landlock";
  for (const job of supervisor.jobs.jobs) job.run_id = 43;
  supervisor.image.Config = {
    Labels: {
      "org.opencontainers.image.source": "https://github.com/asabla/dataground",
      "dataground.dev.supervisor-compatibility-source": "d556748771c41cbbd4e4dd7cd9030c798afe2b7d",
      "dataground.dev.supervisor-compatibility-patch":
        "5e97724dd9d9e7fad9abed8a46b9a4d6e06979119998c411daf34b2423056057",
      "dataground.dev.certification-eligible": "false",
    },
  };
  const config = Buffer.from(
    JSON.stringify({ architecture: "arm64", os: "linux", config: supervisor.image.Config }),
  );
  const manifest = Buffer.from(
    JSON.stringify({
      schemaVersion: 2,
      mediaType: "application/vnd.oci.image.manifest.v1+json",
      config: {
        mediaType: "application/vnd.oci.image.config.v1+json",
        size: config.length,
        digest: `sha256:${hash(config)}`,
      },
    }),
  );
  const publication = { ...scope, digest: `sha256:${hash(manifest)}`, runId: "43" };
  supervisor.result.statement.subject[0] = { name: repository, digest: { sha256: hash(manifest) } };
  supervisor.image.Id = `sha256:${hash(config)}`;
  supervisor.image.RepoDigests = [`${repository}@${publication.digest}`];
  Object.assign(base.artifacts, {
    supervisorManifest: manifest,
    supervisorImageConfig: config,
    supervisorBundle: Buffer.from("synthetic supervisor bundle"),
  });
  const diagnostic = JSON.parse(base.artifacts.diagnostic);
  diagnostic.schemaVersion = "dataground.dev.openshell-runtime-diagnostic/v5";
  diagnostic.policySource = {
    profile: "rosetta-development/v1",
    compilerSourceCommit: "320158f1e4a4eea378d82c1527f4a7af5fb9855b",
    inputSHA256: "b2895b9172c50ba7a5fdf574cebdf6789258cc8ce9f90ce5ad8f2b1ff0a825ab",
  };
  diagnostic.supervisorCandidate = {
    profile: "openshell-supervisor-candidate/v1",
    sourceCommit: "d556748771c41cbbd4e4dd7cd9030c798afe2b7d",
    patchSHA256: "5e97724dd9d9e7fad9abed8a46b9a4d6e06979119998c411daf34b2423056057",
  };
  diagnostic.profile.supervisorImage = supervisor.image.Id;
  diagnostic.profile.runtimePolicySHA256 =
    "a1d56c0470c3264c4c37183352d783ebb67911d92ef2eb6ec5f7c76c61f69f39";
  const gateway = readFileSync(
    new URL("../deploy/openshell/runtime-conformance/gateway.toml", import.meta.url),
    "utf8",
  );
  diagnostic.profile.gatewayConfigSHA256 = hash(
    gateway.replace(JSON.parse(profile).artifacts.supervisor, supervisor.image.Id),
  );
  base.artifacts.diagnostic = Buffer.from(JSON.stringify(diagnostic));
  base.trust.contract = "dataground.local-runtime-acceptance-trust/v2";
  base.trust.profile = "openshell-codex-strict-candidate-development/v1";
  base.trustBytes = canonicalJSON(base.trust);
  base.expected.trustProfileSHA256 = hash(base.trustBytes);
  Object.assign(base.statement, {
    contract: "dataground.local-runtime-acceptance-statement/v2",
    profile: base.trust.profile,
    trustProfileSHA256: hash(base.trustBytes),
    diagnosticSHA256: hash(base.artifacts.diagnostic),
    supervisor: {
      publication,
      localImageId: supervisor.image.Id,
      configSHA256: hash(config),
      bundleSHA256: hash(base.artifacts.supervisorBundle),
    },
  });
  const calls = [];
  const run = (command, args, options) => {
    calls.push([command, args, options]);
    const selected = args.some(
      (arg) => arg.includes(repository) || arg.includes(workflow) || arg.includes("/runs/43/"),
    )
      ? supervisor
      : base.f;
    return selected.run(command, args);
  };
  return { ...base, supervisor, diagnostic, run, calls };
}

test("strict acceptance requires both completed publications and binds the strict topology", () => {
  const f = strictInputs();
  const message = prepareAcceptance(
    canonicalJSON(f.statement),
    f.trustBytes,
    f.artifacts,
    f.expected,
    f.run,
  );
  assert.equal(
    message.toString(),
    `DataGround local candidate runtime acceptance v2\n${canonicalJSON(f.statement)}`,
  );
  assert.equal(
    f.calls.filter(([command, args]) => command === "docker" && args[0] === "pull").length,
    2,
  );
  f.calls.length = 0;
  const envelope = f.envelopeFor();
  const accepted = verifyAcceptance(
    envelope,
    f.trustBytes,
    f.artifacts,
    { ...f.expected, envelopeSHA256: hash(envelope) },
    f.run,
  );
  assert.equal(accepted.profile, "openshell-codex-strict-candidate-development/v1");
  assert.equal(accepted.supervisorLocalImageId, f.supervisor.image.Id);
  assert.equal(
    accepted.supervisorImage,
    `ghcr.io/asabla/dataground-supervisor-candidate@${f.statement.supervisor.publication.digest}`,
  );
  assert.equal(accepted.gatewayConfigSHA256, f.diagnostic.profile.gatewayConfigSHA256);
  assert.equal(accepted.enforcementDigest, `sha256:${f.diagnostic.profile.runtimePolicySHA256}`);
  assert.equal(accepted.certificationEligible, false);
  assert.equal(f.calls.length, 4);
  assert.ok(
    f.calls.every(
      ([command, args]) => command === "gh" && ["--version", "attestation"].includes(args[0]),
    ),
  );
});

test("strict preparation rejects failed supervisor publication, wrong image, and substituted signer", () => {
  for (const mutate of [
    (f) => {
      f.supervisor.attempt.conclusion = "failure";
    },
    (f) => {
      f.supervisor.jobs.jobs[0].conclusion = "skipped";
    },
    (f) => {
      f.supervisor.image.Id = `sha256:${"a".repeat(64)}`;
    },
    (f) => {
      f.supervisor.certificate.buildSignerDigest = "a".repeat(40);
    },
  ]) {
    const f = strictInputs();
    mutate(f);
    assert.throws(() =>
      prepareAcceptance(canonicalJSON(f.statement), f.trustBytes, f.artifacts, f.expected, f.run),
    );
  }
});

test("strict acceptance rejects missing or changed supervisor evidence before external verification", () => {
  for (const key of ["supervisorManifest", "supervisorImageConfig", "supervisorBundle"]) {
    for (const missing of [false, true]) {
      const f = strictInputs();
      if (missing) delete f.artifacts[key];
      else f.artifacts[key] = Buffer.concat([f.artifacts[key], Buffer.from(" ")]);
      const envelope = f.envelopeFor();
      assert.throws(() =>
        verifyAcceptance(
          envelope,
          f.trustBytes,
          f.artifacts,
          { ...f.expected, envelopeSHA256: hash(envelope) },
          f.run,
        ),
      );
      assert.equal(f.calls.length, 0);
    }
  }
});

test("strict acceptance rejects oversized snapshots and unbound supervisor identities", () => {
  const mutations = [
    (f) => {
      f.artifacts.supervisorManifest = Buffer.alloc((1 << 20) + 1);
    },
    (f) => {
      f.artifacts.supervisorImageConfig = Buffer.alloc((1 << 20) + 1);
    },
    (f) => {
      f.artifacts.supervisorBundle = Buffer.alloc((4 << 20) + 1);
    },
    (f) => {
      f.statement.supervisor.localImageId = `sha256:${"0".repeat(64)}`;
    },
    (f) => {
      f.statement.supervisor.publication.architecture = "amd64";
    },
    (f) => {
      f.statement.publication.architecture = "amd64";
    },
    (f) => {
      f.statement.supervisor.extra = true;
    },
    (f) => {
      f.expected.scope = { ...f.expected.scope, isolationDomainId: "iso_abcdefghij0123456789" };
    },
    (f) => {
      f.expected.minimumGeneration = f.statement.generation + 1;
    },
    (f) => {
      f.expected.rejectedAcceptanceIds.add(f.statement.acceptanceId);
    },
  ];
  for (const mutate of mutations) {
    const f = strictInputs();
    mutate(f);
    const envelope = f.envelopeFor();
    assert.throws(() =>
      verifyAcceptance(
        envelope,
        f.trustBytes,
        f.artifacts,
        { ...f.expected, envelopeSHA256: hash(envelope) },
        f.run,
      ),
    );
    assert.equal(f.calls.length, 0);
  }
});

test("a strict reviewer signature cannot replace supervisor provenance or extend expiry during verification", () => {
  const invalid = strictInputs();
  invalid.supervisor.certificate.sourceRepositoryDigest = "0".repeat(40);
  const rejectedEnvelope = invalid.envelopeFor();
  assert.throws(
    () =>
      verifyAcceptance(
        rejectedEnvelope,
        invalid.trustBytes,
        invalid.artifacts,
        { ...invalid.expected, envelopeSHA256: hash(rejectedEnvelope) },
        invalid.run,
      ),
    /No verified attestation/,
  );

  for (const preparing of [false, true]) {
    const f = strictInputs();
    const envelope = f.envelopeFor();
    const expected = { ...f.expected, envelopeSHA256: hash(envelope) };
    const run = (command, args, options) => {
      const result = f.run(command, args, options);
      if (
        args[0] === "attestation" &&
        args.some((arg) => arg.includes("openshell-supervisor-compatibility.yml"))
      ) {
        expected.now = Date.parse(f.statement.expiresAt);
      }
      return result;
    };
    assert.throws(
      () =>
        preparing
          ? prepareAcceptance(canonicalJSON(f.statement), f.trustBytes, f.artifacts, expected, run)
          : verifyAcceptance(envelope, f.trustBytes, f.artifacts, expected, run),
      /expired/,
    );
  }
});

test("strict acceptance cannot reuse legacy trust, signing domain, or downgrade its diagnostic", () => {
  for (const kind of ["trust", "domain", "diagnostic", "gateway", "policy", "envelope"]) {
    const f = strictInputs();
    if (kind === "trust") {
      f.trust.contract = "dataground.local-runtime-acceptance-trust/v1";
      delete f.trust.profile;
      f.trustBytes = canonicalJSON(f.trust);
      f.expected.trustProfileSHA256 = hash(f.trustBytes);
      f.statement.trustProfileSHA256 = hash(f.trustBytes);
    }
    if (["diagnostic", "gateway", "policy"].includes(kind)) {
      if (kind === "diagnostic")
        f.diagnostic.schemaVersion = "dataground.dev.openshell-runtime-diagnostic/v3";
      if (kind === "gateway") f.diagnostic.profile.gatewayConfigSHA256 = "0".repeat(64);
      if (kind === "policy")
        f.diagnostic.profile.runtimePolicySHA256 = record.profile.runtimePolicySHA256;
      f.artifacts.diagnostic = Buffer.from(JSON.stringify(f.diagnostic));
      f.statement.diagnosticSHA256 = hash(f.artifacts.diagnostic);
    }
    let envelope = f.envelopeFor();
    if (kind === "domain" || kind === "envelope") {
      const value = JSON.parse(envelope);
      if (kind === "envelope") value.contract = "dataground.local-runtime-acceptance-envelope/v1";
      else
        value.signature.value = sign(
          null,
          Buffer.concat([
            Buffer.from("DataGround local candidate runtime acceptance v1\n"),
            canonicalJSON(f.statement),
          ]),
          f.privateKey,
        ).toString("base64");
      envelope = canonicalJSON(value);
    }
    assert.throws(
      () =>
        verifyAcceptance(
          envelope,
          f.trustBytes,
          f.artifacts,
          { ...f.expected, envelopeSHA256: hash(envelope) },
          f.run,
        ),
      kind,
    );
    assert.equal(f.calls.length, 0);
  }
});

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

function bindWorkspaceImage(f) {
  const config = Buffer.from(
    JSON.stringify({ architecture: "arm64", os: "linux", config: f.f.image.Config }),
  );
  const manifest = Buffer.from(
    JSON.stringify({
      schemaVersion: 2,
      mediaType: "application/vnd.oci.image.manifest.v1+json",
      config: {
        mediaType: "application/vnd.oci.image.config.v1+json",
        size: config.length,
        digest: `sha256:${hash(config)}`,
      },
    }),
  );
  f.artifacts.imageConfig = config;
  f.artifacts.manifest = manifest;
  f.statement.configSHA256 = hash(config);
  f.statement.publication.digest = `sha256:${hash(manifest)}`;
  f.statement.localImageId = `sha256:${hash(config)}`;
  f.f.image.Id = f.statement.localImageId;
  f.f.image.RepoDigests = [`${imageRepository}@${f.statement.publication.digest}`];
  f.f.result.statement.subject[0].digest.sha256 = hash(manifest);
  f.diagnostic.profile.sandboxImage = f.statement.localImageId;
  f.artifacts.diagnostic = Buffer.from(JSON.stringify(f.diagnostic));
  f.statement.diagnosticSHA256 = hash(f.artifacts.diagnostic);
}

function workspaceInputs() {
  const f = strictInputs();
  f.trust.contract = "dataground.local-runtime-acceptance-trust/v3";
  f.trust.profile = "openshell-codex-workspace-candidate-development/v1";
  f.trustBytes = canonicalJSON(f.trust);
  f.expected.trustProfileSHA256 = hash(f.trustBytes);
  Object.assign(f.statement, {
    contract: "dataground.local-runtime-acceptance-statement/v3",
    profile: f.trust.profile,
    trustProfileSHA256: hash(f.trustBytes),
    workspaceSourceContract: "dataground.workspace-source-materialization/v1",
  });
  f.diagnostic.schemaVersion = "dataground.dev.openshell-runtime-diagnostic/v6";
  f.diagnostic.workspaceSource = {
    profile: "openshell-workspace-source-diagnostic/v1",
    runId: f.diagnostic.run.id,
    startedAt: new Date(Date.parse(f.diagnostic.run.startedAt) - 1000).toISOString(),
    finishedAt: new Date(Date.parse(f.diagnostic.run.startedAt) - 1).toISOString(),
    archiveDigest: "sha256:923eacd2ade97fcaa909d0427e23a1a868f5e7ee1c15f82c476d2207bfc7c613",
    checks: [
      "cross-domain-denial",
      "source-installation",
      "exact-read-back",
      "substitution-denial",
      "native-source-content",
      "native-source-executable",
    ],
  };
  f.f.image.Config.Labels["dataground.dev.workspace-source-contract"] =
    f.statement.workspaceSourceContract;
  f.f.image.Config.Labels["dataground.dev.workspace-source-revision"] =
    f.statement.publication.sourceCommit;
  bindWorkspaceImage(f);
  return f;
}

function verifyWorkspaceFixture(f, envelope = f.envelopeFor(), run = f.run) {
  return verifyAcceptance(
    envelope,
    f.trustBytes,
    f.artifacts,
    { ...f.expected, envelopeSHA256: hash(envelope) },
    run,
  );
}

test("source acceptance binds the helper image and v6 diagnostic under a distinct signature domain", () => {
  const f = workspaceInputs();
  const message = prepareAcceptance(
    canonicalJSON(f.statement),
    f.trustBytes,
    f.artifacts,
    f.expected,
    f.run,
  );
  assert.equal(
    message.toString(),
    `DataGround local candidate runtime acceptance v3\n${canonicalJSON(f.statement)}`,
  );
  assert.equal(
    f.calls.filter(([command, args]) => command === "docker" && args[0] === "pull").length,
    2,
  );
  f.calls.length = 0;
  const accepted = verifyWorkspaceFixture(f);
  assert.equal(accepted.profile, "openshell-codex-workspace-candidate-development/v1");
  assert.equal(accepted.workspaceSourceContract, "dataground.workspace-source-materialization/v1");
  assert.equal(accepted.image, `${imageRepository}@${f.statement.publication.digest}`);
  assert.equal(accepted.supervisorLocalImageId, f.supervisor.image.Id);
  assert.equal(accepted.gatewayConfigSHA256, f.diagnostic.profile.gatewayConfigSHA256);
  assert.equal(accepted.certificationEligible, false);
  assert.equal(f.calls.length, 4);
  assert.equal(
    f.calls.some(([command]) => command === "docker"),
    false,
  );
});

test("a valid source acceptance signature cannot substitute a source-free or mismatched helper image", () => {
  for (const mutation of [
    (f) => {
      delete f.f.image.Config.Labels["dataground.dev.workspace-source-contract"];
    },
    (f) => {
      f.f.image.Config.Labels["dataground.dev.workspace-source-contract"] =
        "dataground.workspace-source-materialization/v2";
    },
    (f) => {
      delete f.f.image.Config.Labels["dataground.dev.workspace-source-revision"];
    },
    (f) => {
      f.f.image.Config.Labels["dataground.dev.workspace-source-revision"] =
        f.statement.sourceRevision;
    },
    (f) => {
      f.f.image.Config.User = "root";
    },
  ]) {
    const f = workspaceInputs();
    mutation(f);
    // Bind even the substituted image into a valid signed manifest and local
    // receipt. Failure must come from the independently selected helper profile.
    bindWorkspaceImage(f);
    assert.throws(() => verifyWorkspaceFixture(f), /configuration.*signed image/i);
    assert.throws(() =>
      prepareAcceptance(canonicalJSON(f.statement), f.trustBytes, f.artifacts, f.expected, f.run),
    );
  }
});

test("source acceptance preserves exact scope, generation, revocation, and both publication gates", () => {
  for (const mutate of [
    (f) => {
      f.expected.scope = { ...f.expected.scope, isolationDomainId: "iso_abcdefghij0123456789" };
    },
    (f) => {
      f.expected.minimumGeneration = 4;
    },
    (f) => {
      f.expected.rejectedAcceptanceIds.add(f.statement.acceptanceId);
    },
    (f) => {
      f.expected.now = Date.parse(f.statement.expiresAt);
    },
    (f) => {
      f.expected.trustProfileSHA256 = "0".repeat(64);
    },
    (f) => {
      f.supervisor.certificate.sourceRepositoryDigest = "0".repeat(40);
    },
  ]) {
    const f = workspaceInputs();
    mutate(f);
    assert.throws(() => verifyWorkspaceFixture(f));
  }
  for (const select of [(f) => f.f, (f) => f.supervisor]) {
    const f = workspaceInputs();
    select(f).jobs.jobs[1].conclusion = "failure";
    assert.throws(
      () =>
        prepareAcceptance(canonicalJSON(f.statement), f.trustBytes, f.artifacts, f.expected, f.run),
      /publication|jobs|attempt/i,
    );
  }
});

test("source acceptance rejects diagnostic downgrade, missing checks, altered chronology and legacy trust", () => {
  for (const mutate of [
    (f) => {
      f.diagnostic.schemaVersion = "dataground.dev.openshell-runtime-diagnostic/v5";
      delete f.diagnostic.workspaceSource;
    },
    (f) => {
      delete f.diagnostic.workspaceSource;
    },
    (f) => {
      f.diagnostic.workspaceSource.runId = "f".repeat(32);
    },
    (f) => {
      f.diagnostic.workspaceSource.checks.pop();
    },
    (f) => {
      f.diagnostic.workspaceSource.finishedAt = f.diagnostic.run.finishedAt;
    },
    (f) => {
      f.diagnostic.cleanup.sandbox.status = "retained";
    },
    (f) => {
      delete f.statement.workspaceSourceContract;
    },
    (f) => {
      f.statement.workspaceSourceContract = "dataground.workspace-source-materialization/v2";
    },
    (f) => {
      f.statement.publication.architecture = "amd64";
    },
    (f) => {
      f.trust.contract = "dataground.local-runtime-acceptance-trust/v2";
      f.trust.profile = "openshell-codex-strict-candidate-development/v1";
    },
  ]) {
    const f = workspaceInputs();
    mutate(f);
    f.artifacts.diagnostic = Buffer.from(JSON.stringify(f.diagnostic));
    f.statement.diagnosticSHA256 = hash(f.artifacts.diagnostic);
    f.trustBytes = canonicalJSON(f.trust);
    f.expected.trustProfileSHA256 = hash(f.trustBytes);
    f.statement.trustProfileSHA256 = hash(f.trustBytes);
    assert.throws(() => verifyWorkspaceFixture(f));
    assert.equal(f.calls.length, 0);
  }
  const f = workspaceInputs();
  const envelope = JSON.parse(f.envelopeFor());
  envelope.signature.value = sign(
    null,
    Buffer.concat([
      Buffer.from("DataGround local candidate runtime acceptance v2\n"),
      canonicalJSON(f.statement),
    ]),
    f.privateKey,
  ).toString("base64");
  assert.throws(() => verifyWorkspaceFixture(f, canonicalJSON(envelope)), /signature/);
});

test("source acceptance rechecks expiry after external image proof verification", () => {
  const f = workspaceInputs();
  const envelope = f.envelopeFor();
  const expected = { ...f.expected, envelopeSHA256: hash(envelope) };
  const run = (command, args, options) => {
    const result = f.run(command, args, options);
    if (
      args[0] === "attestation" &&
      args.some((arg) => arg.includes("openshell-supervisor-compatibility.yml"))
    )
      expected.now = Date.parse(f.statement.expiresAt);
    return result;
  };
  assert.throws(
    () => verifyAcceptance(envelope, f.trustBytes, f.artifacts, expected, run),
    /expired/,
  );
});

test("source preparation checks the pulled helper metadata independently of the offline proof", () => {
  const f = workspaceInputs();
  f.f.image.Config = structuredClone(f.f.image.Config);
  delete f.f.image.Config.Labels["dataground.dev.workspace-source-contract"];
  assert.throws(
    () =>
      prepareAcceptance(canonicalJSON(f.statement), f.trustBytes, f.artifacts, f.expected, f.run),
    /required architecture and isolation metadata/i,
  );
  assert.equal(
    f.calls.some(([command, args]) => command === "docker" && args[0] === "pull"),
    true,
  );
});

function ownedDiagnostic(f) {
  f.diagnostic.schemaVersion = "dataground.dev.openshell-runtime-diagnostic/v7";
  const check = f.diagnostic.checks.find((item) => item.name === "artifact-export");
  f.diagnostic.workspaceExport = {
    profile: "openshell-owned-workspace-diagnostic/v1",
    runId: f.diagnostic.run.id,
    startedAt: check.startedAt,
    finishedAt: check.finishedAt,
    ownedSessions: 11,
    changes: {
      schemaVersion: "dataground.workspace-changes/v1",
      digest: `sha256:${"d".repeat(64)}`,
      sizeBytes: 1800,
      baseArchiveDigest: f.diagnostic.workspaceSource.archiveDigest,
      resultArchiveDigest: `sha256:${"e".repeat(64)}`,
      changeCount: 5,
      entryCount: 6,
      sourceBytes: 15,
    },
    checks: [
      "owned-model-sessions",
      "terminal-owner-acknowledgements",
      "detached-writer-observed",
      "binary-change-export",
      "exact-repeated-export",
      "restored-tree",
      "cross-domain-denial",
    ],
  };
  f.artifacts.diagnostic = Buffer.from(JSON.stringify(f.diagnostic));
  f.statement.diagnosticSHA256 = hash(f.artifacts.diagnostic);
}

test("source acceptance cannot authorize the owned-export diagnostic", () => {
  const f = workspaceInputs();
  ownedDiagnostic(f);
  assert.throws(() =>
    prepareAcceptance(canonicalJSON(f.statement), f.trustBytes, f.artifacts, f.expected, f.run),
  );
  assert.throws(() => verifyWorkspaceFixture(f));
});

function ownedInputs() {
  const f = workspaceInputs();
  ownedDiagnostic(f);
  f.trust.contract = "dataground.local-runtime-acceptance-trust/v4";
  f.trust.profile = "openshell-codex-workspace-export-candidate-development/v1";
  f.trustBytes = canonicalJSON(f.trust);
  f.expected.trustProfileSHA256 = hash(f.trustBytes);
  Object.assign(f.statement, {
    contract: "dataground.local-runtime-acceptance-statement/v4",
    profile: f.trust.profile,
    trustProfileSHA256: hash(f.trustBytes),
    nativeOwnerContract: "dataground.native-owner/v1",
    workspaceChangesContract: "dataground.workspace-changes/v1",
  });
  Object.assign(f.f.image.Config.Labels, {
    "dataground.dev.native-owner-contract": f.statement.nativeOwnerContract,
    "dataground.dev.workspace-change-contract": f.statement.workspaceChangesContract,
    "dataground.dev.workspace-export-revision": f.statement.publication.sourceCommit,
  });
  bindWorkspaceImage(f);
  return f;
}

test("owned acceptance binds the full helper image and v7 diagnostic under a separate signature", () => {
  const f = ownedInputs();
  const message = prepareAcceptance(
    canonicalJSON(f.statement),
    f.trustBytes,
    f.artifacts,
    f.expected,
    f.run,
  );
  assert.equal(
    message.toString(),
    `DataGround local candidate runtime acceptance v4\n${canonicalJSON(f.statement)}`,
  );
  assert.equal(
    f.calls.filter(([command, args]) => command === "docker" && args[0] === "pull").length,
    2,
  );
  f.calls.length = 0;
  const result = verifyWorkspaceFixture(f);
  assert.equal(result.profile, f.trust.profile);
  assert.equal(result.nativeOwnerContract, "dataground.native-owner/v1");
  assert.equal(result.workspaceChangesContract, "dataground.workspace-changes/v1");
  assert.equal(result.workspaceSourceContract, "dataground.workspace-source-materialization/v1");
  assert.equal(result.supervisorLocalImageId, f.supervisor.image.Id);
  assert.equal(result.certificationEligible, false);
  assert.equal(result.deploymentScope, "loopback-development-only");
  assert.equal(f.calls.length, 4);
  assert.equal(
    f.calls.some(([command]) => command === "docker"),
    false,
  );
});

test("owned acceptance rejects signed helper substitutions and mixed helper revisions", () => {
  for (const key of [
    "dataground.dev.native-owner-contract",
    "dataground.dev.workspace-change-contract",
    "dataground.dev.workspace-source-contract",
    "dataground.dev.workspace-export-revision",
    "dataground.dev.workspace-source-revision",
  ]) {
    for (const replacement of [undefined, "substituted"]) {
      const f = ownedInputs();
      if (replacement === undefined) delete f.f.image.Config.Labels[key];
      else f.f.image.Config.Labels[key] = replacement;
      bindWorkspaceImage(f);
      assert.throws(() => verifyWorkspaceFixture(f), /configuration.*signed image/i);
      assert.throws(() =>
        prepareAcceptance(canonicalJSON(f.statement), f.trustBytes, f.artifacts, f.expected, f.run),
      );
    }
  }
  const f = ownedInputs();
  f.f.image.Config = structuredClone(f.f.image.Config);
  delete f.f.image.Config.Labels["dataground.dev.native-owner-contract"];
  assert.throws(
    () =>
      prepareAcceptance(canonicalJSON(f.statement), f.trustBytes, f.artifacts, f.expected, f.run),
    /required architecture and isolation metadata/i,
  );
});

test("owned acceptance rejects missing cleanup, incomplete export and diagnostic downgrades", () => {
  for (const mutate of [
    (f) => {
      f.diagnostic.schemaVersion = "dataground.dev.openshell-runtime-diagnostic/v6";
      delete f.diagnostic.workspaceExport;
    },
    (f) => {
      delete f.diagnostic.workspaceExport;
    },
    (f) => {
      delete f.diagnostic.workspaceSource;
    },
    (f) => {
      f.diagnostic.workspaceExport.ownedSessions--;
    },
    (f) => {
      f.diagnostic.workspaceExport.runId = "f".repeat(32);
    },
    (f) => {
      f.diagnostic.workspaceExport.finishedAt = f.diagnostic.run.finishedAt;
    },
    (f) => {
      f.diagnostic.workspaceExport.changes.baseArchiveDigest = `sha256:${"f".repeat(64)}`;
    },
    (f) => {
      f.diagnostic.workspaceExport.checks.pop();
    },
    (f) => {
      f.diagnostic.cleanup.sandbox.status = "retained";
    },
    (f) => {
      delete f.statement.nativeOwnerContract;
    },
    (f) => {
      f.statement.workspaceChangesContract = "dataground.workspace-changes/v2";
    },
    (f) => {
      f.statement.publication.architecture = "amd64";
    },
    (f) => {
      f.statement.certificationEligible = true;
    },
  ]) {
    const f = ownedInputs();
    mutate(f);
    f.artifacts.diagnostic = Buffer.from(JSON.stringify(f.diagnostic));
    f.statement.diagnosticSHA256 = hash(f.artifacts.diagnostic);
    assert.throws(() => verifyWorkspaceFixture(f));
    assert.equal(f.calls.length, 0);
  }
});

test("owned acceptance cannot reuse older signing trust or signature domains", () => {
  for (const version of [1, 2, 3]) {
    const f = ownedInputs();
    const envelope = JSON.parse(f.envelopeFor());
    envelope.signature.value = sign(
      null,
      Buffer.concat([
        Buffer.from(`DataGround local candidate runtime acceptance v${version}\n`),
        canonicalJSON(f.statement),
      ]),
      f.privateKey,
    ).toString("base64");
    assert.throws(() => verifyWorkspaceFixture(f, canonicalJSON(envelope)), /signature/);
    f.trust.contract = `dataground.local-runtime-acceptance-trust/v${version}`;
    f.trustBytes = canonicalJSON(f.trust);
    f.expected.trustProfileSHA256 = hash(f.trustBytes);
    f.statement.trustProfileSHA256 = hash(f.trustBytes);
    assert.throws(() => verifyWorkspaceFixture(f));
    assert.equal(f.calls.length, 0);
  }
});

test("owned acceptance retains scope, revocation, expiry and both publication gates", () => {
  for (const mutate of [
    (f) => {
      f.expected.scope = { ...f.expected.scope, revisionId: "rev_abcdefghij0123456789" };
    },
    (f) => {
      f.expected.minimumGeneration++;
    },
    (f) => {
      f.expected.rejectedAcceptanceIds.add(f.statement.acceptanceId);
    },
    (f) => {
      f.expected.trustProfileSHA256 = "0".repeat(64);
    },
    (f) => {
      f.expected.now = Date.parse(f.statement.expiresAt);
    },
    (f) => {
      f.supervisor.certificate.sourceRepositoryDigest = "0".repeat(40);
    },
    (f) => {
      f.f.certificate.sourceRepositoryDigest = "0".repeat(40);
    },
  ]) {
    const f = ownedInputs();
    mutate(f);
    assert.throws(() => verifyWorkspaceFixture(f));
  }
  for (const select of [(f) => f.f, (f) => f.supervisor]) {
    const f = ownedInputs();
    select(f).jobs.jobs[1].conclusion = "failure";
    assert.throws(
      () =>
        prepareAcceptance(canonicalJSON(f.statement), f.trustBytes, f.artifacts, f.expected, f.run),
      /publication|jobs|attempt/i,
    );
  }
  for (const prepare of [false, true]) {
    const f = ownedInputs();
    const run = (command, args, options) => {
      const result = f.run(command, args, options);
      f.expected.now = Date.parse(f.statement.expiresAt);
      return result;
    };
    const envelope = f.envelopeFor();
    f.expected.envelopeSHA256 = hash(envelope);
    assert.throws(
      () =>
        prepare
          ? prepareAcceptance(
              canonicalJSON(f.statement),
              f.trustBytes,
              f.artifacts,
              f.expected,
              run,
            )
          : verifyAcceptance(envelope, f.trustBytes, f.artifacts, f.expected, run),
      /expired/,
    );
  }
});
