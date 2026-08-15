import assert from "node:assert/strict";
import {
  chmodSync,
  lstatSync,
  mkdtempSync,
  realpathSync,
  rmSync,
  symlinkSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { ensureLocalGatewayJWT, validateLocalGatewayJWT } from "./openshell-local.mjs";

function testRoot(t) {
  const root = mkdtempSync(join(realpathSync(tmpdir()), "dataground-openshell-local-test-"));
  chmodSync(root, 0o700);
  t.after(() => rmSync(root, { recursive: true, force: true }));
  return root;
}

test("creates and reuses one private matching gateway JWT keypair", (t) => {
  const root = testRoot(t);
  const state = join(root, "state");
  const jwtPath = ensureLocalGatewayJWT(state);
  assert.equal(ensureLocalGatewayJWT(state), jwtPath);
  for (const name of ["signing.pem", "public.pem", "kid"]) {
    assert.equal(lstatSync(join(jwtPath, name)).mode & 0o777, 0o600);
  }
});

test("rejects a symbolic-link substitution for a gateway JWT key", (t) => {
  const root = testRoot(t);
  const jwtPath = ensureLocalGatewayJWT(join(root, "state"));
  const signingPath = join(jwtPath, "signing.pem");
  const decoyPath = join(root, "decoy.pem");
  writeFileSync(decoyPath, "not-a-private-key\n", { mode: 0o600 });
  unlinkSync(signingPath);
  symlinkSync(decoyPath, signingPath);
  assert.throws(() => validateLocalGatewayJWT(jwtPath), /must not be symbolic links/u);
});
