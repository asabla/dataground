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
