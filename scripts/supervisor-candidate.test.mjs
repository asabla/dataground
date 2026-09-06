import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { existsSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { verifyPublication } from "./check-codex-candidate-publication.mjs";
import {
  verifySupervisorAttestation,
  verifySupervisorPublication,
} from "./check-supervisor-candidate.mjs";
import { fixture, scope } from "./codex-candidate-fixture.mjs";

const repository = "ghcr.io/asabla/dataground-supervisor-candidate";
const workflow = ".github/workflows/openshell-supervisor-compatibility.yml";
const signer = `https://github.com/asabla/dataground/${workflow}@refs/heads/main`;
const sourceLabel = "dataground.dev.supervisor-compatibility-source";
const patchLabel = "dataground.dev.supervisor-compatibility-patch";
const hash = (value) => createHash("sha256").update(value).digest("hex");

function supervisorFixture() {
  const f = fixture();
  for (const key of ["subjectAlternativeName", "buildSignerURI", "buildConfigURI"])
    f.certificate[key] = signer;
  f.result.statement.subject[0].name = repository;
  f.attempt.path = workflow;
  f.jobs.jobs[0].name = "strict-landlock";
  delete f.image.Config.User;
  delete f.image.Config.Labels["dataground.dev.codex-compatibility-source"];
  f.image.Config.Labels[sourceLabel] = "d556748771c41cbbd4e4dd7cd9030c798afe2b7d";
  f.image.Config.Labels[patchLabel] =
    "5e97724dd9d9e7fad9abed8a46b9a4d6e06979119998c411daf34b2423056057";
  f.image.RepoDigests = [`${repository}@${scope.digest}`];
  return f;
}

test("supervisor publication binds its signer, successful strict job and exact patched image", () => {
  const f = supervisorFixture();
  const result = verifySupervisorPublication(scope, f.run);
  assert.equal(result.candidateProfile, "openshell-supervisor-candidate/v1");
  assert.equal(result.image, `${repository}@${scope.digest}`);
  assert.equal(result.certificationEligible, false);
  const flags = f.calls[1][1];
  assert.equal(flags[2], `oci://${repository}@${scope.digest}`);
  assert.equal(flags[flags.indexOf("--signer-workflow") + 1], `asabla/dataground/${workflow}`);
  assert.equal(f.calls[4][0], "docker");
  assert.equal(f.calls[4][1].at(-1), `${repository}@${scope.digest}`);
});

test("Codex and supervisor publications cannot substitute for each other", () => {
  const codex = fixture();
  const supervisor = supervisorFixture();
  assert.throws(() => verifySupervisorPublication(scope, codex.run), /No verified attestation/);
  assert.throws(() => verifyPublication(scope, supervisor.run), /No verified attestation/);
  for (const f of [codex, supervisor])
    assert.equal(
      f.calls.some(([command]) => command === "docker"),
      false,
    );
  assert.throws(
    () => verifyPublication(scope, () => assert.fail("unexpected effect"), "unregistered"),
    /Unknown candidate profile/,
  );
});

test("supervisor provenance rejects each changed certificate binding before pulling", () => {
  for (const key of Object.keys(supervisorFixture().certificate)) {
    const f = supervisorFixture();
    f.certificate[key] = "substituted";
    assert.throws(() => verifySupervisorPublication(scope, f.run), /No verified attestation/, key);
    assert.equal(
      f.calls.some(([command]) => command === "docker"),
      false,
    );
  }
});

test("signed supervisor images still require the exact completed workflow and both jobs", () => {
  for (const mutate of [
    (f) => {
      f.attempt.conclusion = "failure";
    },
    (f) => {
      f.attempt.run_attempt = 2;
    },
    (f) => {
      f.attempt.path = ".github/workflows/codex-compatibility.yml";
    },
    (f) => {
      f.jobs.jobs[0].name = "native-sandbox";
    },
    (f) => {
      f.jobs.jobs[0].conclusion = "skipped";
    },
    (f) => {
      f.jobs.jobs[1].conclusion = "failure";
    },
    (f) => {
      f.jobs.jobs[1].run_attempt = 2;
    },
  ]) {
    const f = supervisorFixture();
    mutate(f);
    assert.throws(() => verifySupervisorPublication(scope, f.run));
    assert.equal(
      f.calls.some(([command]) => command === "docker"),
      false,
    );
  }
});

test("supervisor metadata and unsupported architecture fail closed", () => {
  for (const mutate of [
    (f) => {
      f.image.Config.Labels[patchLabel] = "0".repeat(64);
    },
    (f) => {
      f.image.Config.Labels[sourceLabel] = "0".repeat(40);
    },
    (f) => {
      f.image.Config.User = "sandbox";
    },
    (f) => {
      f.image.Config.User = null;
    },
    (f) => {
      f.image.Architecture = "amd64";
    },
    (f) => {
      f.image.Config.Labels["dataground.dev.certification-eligible"] = "true";
    },
  ]) {
    const f = supervisorFixture();
    mutate(f);
    assert.throws(() => verifySupervisorPublication(scope, f.run), /Pulled candidate image/);
  }
  assert.throws(
    () =>
      verifySupervisorPublication({ ...scope, architecture: "amd64" }, () =>
        assert.fail("unexpected effect"),
      ),
    /Exact candidate architecture/,
  );
});

function offlineFixture(t, mutate = () => {}) {
  const f = supervisorFixture();
  const config = { architecture: "arm64", os: "linux", config: f.image.Config };
  mutate(config);
  const configBytes = Buffer.from(JSON.stringify(config));
  const manifest = Buffer.from(
    JSON.stringify({
      schemaVersion: 2,
      mediaType: "application/vnd.oci.image.manifest.v1+json",
      config: {
        mediaType: "application/vnd.oci.image.config.v1+json",
        size: configBytes.length,
        digest: `sha256:${hash(configBytes)}`,
      },
    }),
  );
  const directory = mkdtempSync(join(tmpdir(), "dataground-supervisor-attestation-test-"));
  t.after(() => rmSync(directory, { recursive: true }));
  const inputs = { trustedRootSHA256: hash("synthetic roots") };
  for (const [name, bytes] of Object.entries({
    manifest,
    imageConfig: configBytes,
    bundle: "synthetic bundle",
    trustedRoot: "synthetic roots",
  })) {
    inputs[name] = join(directory, name);
    writeFileSync(inputs[name], bytes, { flag: "wx", mode: 0o600 });
  }
  const expected = { ...scope, digest: `sha256:${hash(manifest)}` };
  f.result.statement.subject[0].digest.sha256 = expected.digest.slice(7);
  return { f, expected, inputs };
}

test("offline supervisor proof binds the raw image configuration and uses credential-free snapshots", (t) => {
  const { f, expected, inputs } = offlineFixture(t);
  let directory;
  const result = verifySupervisorAttestation(expected, inputs, (command, args, options) => {
    directory = options.cwd;
    assert.deepEqual(
      Object.keys(options.env).sort(),
      [
        "PATH",
        "GH_HOST",
        "GH_PROMPT_DISABLED",
        "GH_CONFIG_DIR",
        "XDG_STATE_HOME",
        "HTTPS_PROXY",
        "HTTP_PROXY",
      ].sort(),
    );
    assert.equal(options.env.HTTPS_PROXY, "http://127.0.0.1:9");
    return f.run(command, args);
  });
  assert.equal(existsSync(directory), false);
  assert.equal(result.candidateProfile, "openshell-supervisor-candidate/v1");
  assert.equal(result.image.user, "");
  assert.equal(result.image.architecture, "arm64");
  assert.equal(result.publicationCompletionChecked, false);
  assert.equal(result.certificationEligible, false);
});

test("offline supervisor metadata, manifest substitution and wrong signed profile are rejected", (t) => {
  for (const mutate of [
    (config) => {
      config.config.Labels[patchLabel] = "substituted";
    },
    (config) => {
      config.config.User = null;
    },
    (config) => {
      config.architecture = "amd64";
    },
  ]) {
    const { expected, inputs } = offlineFixture(t, mutate);
    assert.throws(
      () =>
        verifySupervisorAttestation(expected, inputs, () => assert.fail("unexpected verification")),
      /configuration/,
    );
  }
  const { f, expected, inputs } = offlineFixture(t);
  f.result.statement.subject[0].name = "ghcr.io/asabla/dataground-codex-candidate";
  assert.throws(
    () => verifySupervisorAttestation(expected, inputs, f.run),
    /No verified attestation/,
  );
  writeFileSync(inputs.imageConfig, "substituted configuration");
  assert.throws(() =>
    verifySupervisorAttestation(expected, inputs, () => assert.fail("unexpected verification")),
  );
});
