import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";

const result = spawnSync("gofmt", ["-d", "internal/reconcile/invocation_authorizer_test.go"], {
  encoding: "utf8",
  shell: false,
});

assert.equal(result.error, undefined, result.error?.message);
assert.equal(result.status, 1, result.stderr);
assert.equal(result.stdout.trim(), "", `Go files need formatting:\n${result.stdout}`);

console.log("Go formatting check passed");
