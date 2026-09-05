import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readdir, readFile } from "node:fs/promises";
import { basename, join, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const schemaDirectory = process.argv[2];
assert.ok(
  schemaDirectory,
  "usage: node scripts/check-codex-schema.mjs <generated-schema-directory>",
);

const profile = JSON.parse(
  await readFile(resolve(root, "deploy/openshell/development-profile.json"), "utf8"),
);
const runtime = profile.runtime;
const files = await listFiles(resolve(schemaDirectory));
assert.equal(files.length, runtime.schemaFileCount, "generated schema file count does not match");

const aggregatePath = files.find((file) => basename(file) === runtime.schemaEvidence.file);
assert.ok(aggregatePath, `${runtime.schemaEvidence.file} is missing`);
const aggregate = JSON.parse(await readFile(aggregatePath, "utf8"));
const canonical = JSON.stringify(sortObjectKeys(aggregate));
const digest = createHash("sha256").update(canonical).digest("hex");
assert.equal(
  digest,
  runtime.schemaEvidence.canonicalSHA256,
  "canonical schema digest does not match",
);

const clientMethods = collectMethods(aggregate);
for (const method of ["initialize", "thread/start", "turn/start", "turn/interrupt"]) {
  assert.ok(clientMethods.has(method), `required client method ${method} is absent`);
}
for (const method of [
  "turn/started",
  "turn/completed",
  "serverRequest/resolved",
  "item/agentMessage/delta",
  "item/started",
  "item/completed",
]) {
  assert.ok(clientMethods.has(method), `required notification ${method} is absent`);
}

const serverRequestPath = files.find((file) => basename(file) === "ServerRequest.json");
assert.ok(serverRequestPath, "ServerRequest.json is missing");
const serverMethods = collectMethods(JSON.parse(await readFile(serverRequestPath, "utf8")));
for (const method of [
  "item/commandExecution/requestApproval",
  "item/fileChange/requestApproval",
  "item/tool/requestUserInput",
]) {
  assert.ok(serverMethods.has(method), `required server request ${method} is absent`);
}

console.log(`Codex ${runtime.version} schema evidence is reproducible and complete.`);

async function listFiles(directoryPath) {
  const entries = await readdir(directoryPath, { withFileTypes: true });
  const nested = await Promise.all(
    entries.map(async (entry) => {
      const path = join(directoryPath, entry.name);
      if (entry.isDirectory()) return listFiles(path);
      return entry.isFile() ? [path] : [];
    }),
  );
  return nested.flat().sort();
}

function sortObjectKeys(value) {
  if (Array.isArray(value)) return value.map(sortObjectKeys);
  if (value === null || typeof value !== "object") return value;
  return Object.fromEntries(
    Object.keys(value)
      .sort()
      .map((key) => [key, sortObjectKeys(value[key])]),
  );
}

function collectMethods(value, methods = new Set()) {
  if (Array.isArray(value)) {
    for (const item of value) collectMethods(item, methods);
    return methods;
  }
  if (value === null || typeof value !== "object") return methods;
  for (const name of value.properties?.method?.enum ?? []) methods.add(name);
  for (const child of Object.values(value)) collectMethods(child, methods);
  return methods;
}
