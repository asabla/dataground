import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import {
  chmodSync,
  existsSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { verifyOfflineAttestation } from "./check-codex-candidate-attestation.mjs";
import { fixture, scope } from "./codex-candidate-fixture.mjs";

const digest = (bytes) => createHash("sha256").update(bytes).digest("hex");

function inputs(t) {
  const directory = mkdtempSync(join(tmpdir(), "dataground-attestation-test-"));
  t.after(() => rmSync(directory, { recursive: true }));
  const files = {
    manifest: join(directory, "manifest.json"),
    bundle: join(directory, "bundle.jsonl"),
    trustedRoot: join(directory, "trusted-root.jsonl"),
    trustedRootSHA256: digest("synthetic roots"),
  };
  writeFileSync(files.manifest, "synthetic manifest", { mode: 0o600 });
  writeFileSync(files.bundle, "synthetic bundle", { mode: 0o600 });
  writeFileSync(files.trustedRoot, "synthetic roots", { mode: 0o600 });
  const expected = { ...scope, digest: `sha256:${digest("synthetic manifest")}` };
  const f = fixture();
  f.result.statement.subject[0].digest.sha256 = expected.digest.slice(7);
  return { files, expected, f };
}

test("offline verification freezes bounded private inputs and inherits no credentials", (t) => {
  const { files, expected, f } = inputs(t);
  let frozenDirectory;
  const result = verifyOfflineAttestation(expected, files, (command, args, options) => {
    assert.equal(command, "gh");
    frozenDirectory = options.cwd;
    assert.equal(statSync(frozenDirectory).mode & 0o777, 0o700);
    assert.deepEqual(
      Object.keys(options.env).sort(),
      [
        "GH_CONFIG_DIR",
        "GH_HOST",
        "GH_PROMPT_DISABLED",
        "HTTPS_PROXY",
        "HTTP_PROXY",
        "PATH",
        "XDG_STATE_HOME",
      ].sort(),
    );
    assert.equal(dirname(options.env.GH_CONFIG_DIR), frozenDirectory);
    assert.equal(dirname(options.env.XDG_STATE_HOME), frozenDirectory);
    assert.equal(options.env.HTTPS_PROXY, "http://127.0.0.1:9");
    if (args[0] === "--version") {
      // Replacement after snapshot acquisition cannot affect gh's inputs.
      writeFileSync(files.manifest, "changed original manifest");
      writeFileSync(files.bundle, "changed original bundle");
      writeFileSync(files.trustedRoot, "changed original roots");
    } else {
      assert.equal(args[0], "attestation");
      for (const [file, content] of [
        [args[2], "synthetic manifest"],
        [args[args.indexOf("--bundle") + 1], "synthetic bundle"],
        [args[args.indexOf("--custom-trusted-root") + 1], "synthetic roots"],
      ]) {
        assert.equal(dirname(file), frozenDirectory);
        assert.equal(statSync(file).mode & 0o777, 0o600);
        assert.equal(readFileSync(file, "utf8"), content);
      }
    }
    return f.run(command, args);
  });
  assert.equal(existsSync(frozenDirectory), false);
  assert.equal(result.publicationCompletionChecked, false);
  assert.equal(result.certificationEligible, false);
  assert.equal(result.bundleSHA256, digest("synthetic bundle"));
  assert.equal(result.imageDigest, expected.digest);
  assert.equal(f.calls.length, 2);
});

test("offline input metadata and independently pinned digests fail before verification", (t) => {
  const mutations = [
    (files) => {
      files.trustedRootSHA256 = "";
    },
    (files) => {
      files.trustedRootSHA256 = "1".repeat(64);
    },
    (files) => writeFileSync(files.manifest, "substituted manifest"),
    (files) => chmodSync(files.bundle, 0o644),
    (files) => {
      rmSync(files.bundle);
      symlinkSync(files.manifest, files.bundle);
    },
    (files) => {
      files.bundle = dirname(files.bundle);
    },
    (files) => writeFileSync(files.bundle, Buffer.alloc((4 << 20) + 1)),
    (files) => writeFileSync(files.bundle, ""),
    (files) => {
      files.manifest = "relative.json";
    },
  ];
  for (const mutate of mutations) {
    const { files, expected } = inputs(t);
    mutate(files);
    let calls = 0;
    assert.throws(() =>
      verifyOfflineAttestation(expected, files, () => {
        calls += 1;
        throw new Error("unexpected verification effect");
      }),
    );
    assert.equal(calls, 0);
  }
});

test("failed signature verification discards snapshots and withholds subprocess output", (t) => {
  const { files, expected } = inputs(t);
  let directory;
  assert.throws(
    () =>
      verifyOfflineAttestation(expected, files, (_command, _args, options) => {
        directory = options.cwd;
        throw new Error("synthetic private subprocess output");
      }),
    (error) => !error.message.includes("synthetic private"),
  );
  assert.equal(existsSync(directory), false);
});

function imageInputs(t, mutate = () => {}) {
  const { files, expected, f } = inputs(t);
  files.imageConfig = join(dirname(files.manifest), "config.json");
  expected.architecture = "arm64";
  const config = { architecture: "arm64", os: "linux", config: f.image.Config };
  const manifest = {
    schemaVersion: 2,
    mediaType: "application/vnd.docker.distribution.manifest.v2+json",
    config: { mediaType: "application/vnd.docker.container.image.v1+json" },
  };
  mutate(config, manifest, expected);
  const bytes = Buffer.from(JSON.stringify(config));
  manifest.config.digest = `sha256:${digest(bytes)}`;
  manifest.config.size = bytes.length;
  const manifestBytes = Buffer.from(JSON.stringify(manifest));
  writeFileSync(files.imageConfig, bytes, { mode: 0o600 });
  writeFileSync(files.manifest, manifestBytes);
  expected.digest = `sha256:${digest(manifestBytes)}`;
  f.result.statement.subject[0].digest.sha256 = expected.digest.slice(7);
  return { files, expected, f, config, manifest };
}

test("offline configuration is bound to the signed manifest for Docker and OCI images", (t) => {
  for (const oci of [false, true]) {
    const { files, expected, f, manifest } = imageInputs(t, (_config, value) => {
      if (oci) {
        value.mediaType = "application/vnd.oci.image.manifest.v1+json";
        value.config.mediaType = "application/vnd.oci.image.config.v1+json";
      }
    });
    const result = verifyOfflineAttestation(expected, files, (command, args) => {
      // Later replacement must not change the verified configuration snapshot.
      writeFileSync(files.imageConfig, "replacement configuration");
      return f.run(command, args);
    });
    assert.deepEqual(result.image, {
      configDigest: manifest.config.digest,
      architecture: "arm64",
      os: "linux",
      user: "sandbox",
    });
    assert.equal(result.publicationCompletionChecked, false);
    assert.equal(result.certificationEligible, false);
  }
});

test("signed configurations with incompatible execution metadata fail before gh", (t) => {
  const mutations = [
    (config) => {
      config.architecture = "amd64";
    },
    (config) => {
      config.os = "windows";
    },
    (config) => {
      config.config.User = "root";
    },
    (config) => {
      delete config.config.User;
    },
    (config) => {
      config.config.Labels["org.opencontainers.image.source"] = "https://example.com";
    },
    (config) => {
      config.config.Labels["dataground.dev.codex-compatibility-source"] = "0".repeat(40);
    },
    (config) => {
      config.config.Labels["dataground.dev.certification-eligible"] = "true";
    },
    (config) => {
      delete config.config.Labels;
    },
    (_config, manifest) => {
      manifest.schemaVersion = 1;
    },
    (_config, manifest) => {
      manifest.mediaType = "application/vnd.oci.image.index.v1+json";
    },
    (_config, manifest) => {
      manifest.config.mediaType = "application/vnd.oci.image.config.v1+json";
    },
    (_config, _manifest, expected) => {
      expected.architecture = "s390x";
    },
    (_config, _manifest, expected) => {
      delete expected.architecture;
    },
  ];
  for (const mutate of mutations) {
    const { files, expected, f } = imageInputs(t, mutate);
    assert.throws(() => verifyOfflineAttestation(expected, files, f.run));
    assert.equal(f.calls.length, 0);
  }
});

test("configuration substitutions and unsafe acquisition fail closed", (t) => {
  for (const mutate of [
    (files) => writeFileSync(files.imageConfig, "{}"),
    (files) =>
      writeFileSync(
        files.imageConfig,
        JSON.stringify(JSON.parse(readFileSync(files.imageConfig)), null, 2),
      ),
    (files) => chmodSync(files.imageConfig, 0o644),
    (files) => {
      rmSync(files.imageConfig);
      symlinkSync(files.bundle, files.imageConfig);
    },
    (files) => writeFileSync(files.imageConfig, Buffer.alloc((1 << 20) + 1)),
    (files) => {
      files.imageConfig = "relative-config.json";
    },
    (files) => {
      files.imageConfig = dirname(files.imageConfig);
    },
  ]) {
    const { files, expected, f } = imageInputs(t);
    mutate(files);
    assert.throws(() => verifyOfflineAttestation(expected, files, f.run));
    assert.equal(f.calls.length, 0);
  }
  const { files, expected, f, manifest } = imageInputs(t);
  manifest.config.size += 1;
  const changed = JSON.stringify(manifest);
  writeFileSync(files.manifest, changed);
  expected.digest = `sha256:${digest(changed)}`;
  assert.throws(() => verifyOfflineAttestation(expected, files, f.run));
  assert.equal(f.calls.length, 0);
});
