import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

async function readJson(relativePath) {
  const absolutePath = path.join(repositoryRoot, relativePath);
  return JSON.parse(await readFile(absolutePath, "utf8"));
}

const openApi = await readJson("contracts/openapi/dataground-api.openapi.json");
assert.equal(openApi.openapi, "3.1.2", "OpenAPI version must remain explicit");
assert.equal(
  openApi.jsonSchemaDialect,
  "https://json-schema.org/draft/2020-12/schema",
  "OpenAPI must use the repository JSON Schema dialect",
);
assert.equal(openApi.servers, undefined, "bootstrap contracts must not publish an endpoint");
assert.ok(Object.keys(openApi.paths).length > 0, "OpenAPI must define at least one path");

const operationMethods = new Set([
  "delete",
  "get",
  "head",
  "options",
  "patch",
  "post",
  "put",
  "trace",
]);
const operationIds = new Set();

for (const [route, pathItem] of Object.entries(openApi.paths)) {
  assert.ok(route.startsWith("/"), `OpenAPI route ${route} must be absolute`);
  const operations = Object.entries(pathItem).filter(([method]) => operationMethods.has(method));
  assert.ok(operations.length > 0, `OpenAPI route ${route} needs an operation`);

  for (const [method, operation] of operations) {
    assert.ok(operation.operationId, `${method.toUpperCase()} ${route} needs an operationId`);
    assert.ok(operation.responses, `${method.toUpperCase()} ${route} needs responses`);
    assert.ok(
      !operationIds.has(operation.operationId),
      `duplicate operationId ${operation.operationId}`,
    );
    operationIds.add(operation.operationId);
  }
}

const releaseManifest = await readJson("contracts/schemas/release-manifest.schema.json");
assert.equal(
  releaseManifest.$schema,
  "https://json-schema.org/draft/2020-12/schema",
  "release manifest must use JSON Schema 2020-12",
);
assert.equal(
  releaseManifest.$id,
  "urn:dataground:schema:release-manifest:v0alpha1",
  "release manifest schema identity must be versioned",
);
assert.ok(
  releaseManifest.required.includes("schemaVersion"),
  "release manifests must identify their schema version",
);

console.log("contract structure checks passed");
