import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

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
assert.equal(openApi.servers, undefined, "contracts must not embed a deployment endpoint");
assert.ok(Object.keys(openApi.paths).length > 0, "OpenAPI must define at least one path");
assert.deepEqual(
  openApi.security,
  [{ BearerAccessToken: [] }],
  "versioned API operations must inherit the bearer authentication requirement",
);
assert.deepEqual(
  openApi.components.securitySchemes?.BearerAccessToken,
  {
    type: "http",
    scheme: "bearer",
    bearerFormat: "JWT",
    description:
      "OIDC/OAuth access token. The deployment-owned authentication boundary validates issuer, audience, credential state, and isolation-domain membership before request processing.",
  },
  "the public bearer authentication contract is missing or inconsistent",
);
assert.equal(
  Object.values(openApi.components.securitySchemes ?? {}).some(
    (scheme) => scheme.type === "apiKey",
  ),
  false,
  "API-key authentication must remain disabled by default",
);

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
const mutationMethods = new Set(["delete", "patch", "post", "put"]);
const unscopedHealthRoutes = new Set(["/livez", "/readyz"]);
const operationIds = new Set();
const operations = new Map();

for (const [route, pathItem] of Object.entries(openApi.paths)) {
  assert.ok(route.startsWith("/"), `OpenAPI route ${route} must be absolute`);
  if (!unscopedHealthRoutes.has(route)) {
    assert.ok(
      route.startsWith("/v1/isolation-domains/{isolationDomainId}/"),
      `resource route ${route} must be isolation-domain scoped`,
    );
  }
  const pathOperations = Object.entries(pathItem).filter(([method]) =>
    operationMethods.has(method),
  );
  assert.ok(pathOperations.length > 0, `OpenAPI route ${route} needs an operation`);

  for (const [method, operation] of pathOperations) {
    assert.ok(operation.operationId, `${method.toUpperCase()} ${route} needs an operationId`);
    assert.ok(operation.responses, `${method.toUpperCase()} ${route} needs responses`);
    assert.ok(
      !operationIds.has(operation.operationId),
      `duplicate operationId ${operation.operationId}`,
    );
    if (unscopedHealthRoutes.has(route)) {
      assert.deepEqual(
        operation.security,
        [],
        `${method.toUpperCase()} ${route} must remain outside API authentication`,
      );
    } else {
      assert.equal(
        operation.security,
        undefined,
        `${method.toUpperCase()} ${route} must inherit the common authentication boundary`,
      );
      for (const status of ["401", "403", "503"]) {
        assert.ok(
          operation.responses[status],
          `${method.toUpperCase()} ${route} must document authentication status ${status}`,
        );
      }
    }
    if (mutationMethods.has(method)) {
      assert.ok(
        operation.parameters?.some(
          (parameter) => parameter.$ref === "#/components/parameters/IdempotencyKey",
        ),
        `${method.toUpperCase()} ${route} must require Idempotency-Key`,
      );
      assert.ok(
        operation.responses["415"],
        `${method.toUpperCase()} ${route} must document unsupported JSON media types`,
      );
    }
    operationIds.add(operation.operationId);
    operations.set(operation.operationId, `${method.toUpperCase()} ${route}`);
  }
}

const serializedContract = JSON.stringify(openApi);
for (const forbiddenName of [
  "gatewayAddress",
  "kubernetesCredentials",
  "providerApiKey",
  "sandboxPort",
]) {
  assert.ok(
    !serializedContract.includes(forbiddenName),
    `public contract exposes ${forbiddenName}`,
  );
}

const releaseManifest = await readJson("contracts/schemas/release-manifest.schema.json");
const authorizationAuditExport = await readJson(
  "contracts/schemas/authorization-audit-export.schema.json",
);
const operatorAuditExport = await readJson("contracts/schemas/operator-audit-export.schema.json");
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

const ajv = new Ajv2020({ allErrors: true, strict: false });
addFormats(ajv);
ajv.addSchema(openApi, "urn:dataground:openapi:v1");
const validateReleaseManifest = ajv.compile(releaseManifest);
const validateAuthorizationAuditExport = ajv.compile(authorizationAuditExport);
const validateOperatorAuditExport = ajv.compile(operatorAuditExport);
const fixtureManifest = await readJson("contracts/fixtures/manifest.json");

for (const fixture of fixtureManifest.fixtures) {
  const value = await readJson(`contracts/fixtures/${fixture.file}`);
  let validate;
  if (fixture.document === "release-manifest") {
    validate = validateReleaseManifest;
  } else if (fixture.document === "authorization-audit-export") {
    validate = validateAuthorizationAuditExport;
  } else if (fixture.document === "operator-audit-export") {
    validate = validateOperatorAuditExport;
  } else {
    validate = ajv.compile({
      $ref: `urn:dataground:openapi:v1#/components/schemas/${fixture.schema}`,
    });
  }
  const actual = validate(value);
  assert.equal(
    actual,
    fixture.valid,
    `${fixture.file} expected valid=${fixture.valid}; ${ajv.errorsText(validate.errors)}`,
  );
  if (
    (fixture.document === "authorization-audit-export" ||
      fixture.document === "operator-audit-export") &&
    fixture.valid
  ) {
    const digest = createHash("sha256").update(JSON.stringify(value.content)).digest("hex");
    assert.equal(
      value.contentSha256,
      `sha256:${digest}`,
      `${fixture.file} must bind its canonical content bytes`,
    );
  }
}

const baseline = await readJson("contracts/compatibility/v1-baseline.json");
for (const [operationId, signature] of Object.entries(baseline.operations)) {
  assert.equal(
    operations.get(operationId),
    signature,
    `breaking operation change for ${operationId}; introduce a new major API instead of changing the baseline`,
  );
}
for (const [schemaName, published] of Object.entries(baseline.schemas)) {
  const schema = openApi.components.schemas[schemaName];
  assert.ok(schema, `published schema ${schemaName} was removed`);
  for (const property of published.properties) {
    assert.ok(
      schema.properties?.[property],
      `published property ${schemaName}.${property} was removed`,
    );
  }
  for (const property of published.required) {
    assert.ok(
      schema.required?.includes(property),
      `required property ${schemaName}.${property} became ambiguous`,
    );
  }
  const newlyRequired = (schema.required ?? []).filter(
    (property) => !published.required.includes(property),
  );
  assert.deepEqual(
    newlyRequired,
    [],
    `breaking required properties added to ${schemaName}: ${newlyRequired.join(", ")}`,
  );
}
for (const [location, publishedValues] of Object.entries(baseline.enums)) {
  const [schemaName, propertyName] = location.split(".");
  assert.deepEqual(
    openApi.components.schemas[schemaName].properties[propertyName].enum,
    publishedValues,
    `breaking enum change for ${location}; use the existing unknown state or introduce a new major API`,
  );
}
for (const [schemaName, pattern] of Object.entries(baseline.patterns)) {
  assert.equal(
    openApi.components.schemas[schemaName].pattern,
    pattern,
    `breaking identifier pattern change for ${schemaName}`,
  );
}
for (const [location, constant] of Object.entries(baseline.constants)) {
  const [schemaName, propertyName] = location.split(".");
  assert.equal(
    openApi.components.schemas[schemaName].properties[propertyName].const,
    constant,
    `breaking version constant change for ${location}`,
  );
}

console.log(
  `contract checks passed (${fixtureManifest.fixtures.length} fixtures, ${baseline.operations ? Object.keys(baseline.operations).length : 0} frozen operations)`,
);
