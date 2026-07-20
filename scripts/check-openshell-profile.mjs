import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const profilePath = resolve(root, "deploy/openshell/development-profile.json");
const profile = JSON.parse(await readFile(profilePath, "utf8"));

const failures = [];
const fail = (message) => failures.push(message);
const digestPattern = /@sha256:[a-f0-9]{64}$/;

if (profile.schemaVersion !== "dataground.dev.openshell-profile/v1") {
  fail("unexpected development profile schema version");
}
if (profile.status !== "blocked" || profile.productionCertifiable !== false) {
  fail("the incomplete development profile must remain blocked and non-certifiable");
}
if (
  profile.topology?.driver !== "docker" ||
  profile.topology?.gatewayEndpoint !== "http://127.0.0.1:8080"
) {
  fail("the initial topology must remain the loopback Docker profile");
}
for (const [name, reference] of Object.entries(profile.artifacts ?? {})) {
  if (typeof reference !== "string" || !digestPattern.test(reference)) {
    fail(`${name} is not pinned to an OCI sha256 digest`);
  }
}
if (profile.enforcement?.mode !== "deny-all") {
  fail("the checked-in enforcement fixture must remain deny-all");
}

const fixturePath = resolve(root, profile.enforcement.fixture);
const fixture = await readFile(fixturePath);
const actualFixtureDigest = createHash("sha256").update(fixture).digest("hex");
if (actualFixtureDigest !== profile.enforcement.sha256) {
  fail("the enforcement fixture does not match its recorded upstream digest");
}
if (profile.runtime?.nativeInterface !== "app-server JSONL over stdio") {
  fail("the first Codex integration must use the native app-server stdio interface");
}
if (
  profile.runtime?.schemaEvidence?.file !== "codex_app_server_protocol.v2.schemas.json" ||
  !/^[a-f0-9]{64}$/.test(profile.runtime?.schemaEvidence?.canonicalSHA256 ?? "") ||
  !profile.runtime?.schemaEvidence?.canonicalization
) {
  fail("the pinned Codex schema must have canonical, reproducible evidence");
}

const compose = await readFile(resolve(root, "deploy/openshell/docker-compose.yml"), "utf8");
const gatewayConfig = await readFile(resolve(root, "deploy/openshell/gateway.toml"), "utf8");
if (!compose.includes(profile.artifacts.gateway) || !compose.includes('"127.0.0.1:8080:8080"')) {
  fail("Docker Compose does not match the pinned loopback gateway profile");
}
if (
  !gatewayConfig.includes(profile.artifacts.supervisor) ||
  !gatewayConfig.includes(profile.artifacts.sandbox)
) {
  fail("gateway configuration does not match the pinned supervisor and sandbox images");
}
if (
  compose.includes(":latest") ||
  gatewayConfig.includes(":latest") ||
  gatewayConfig.includes("enable_bind_mounts = true")
) {
  fail("mutable images or unsafe sandbox bind mounts are present in the local topology");
}
if (!Array.isArray(profile.blockers) || profile.blockers.length === 0) {
  fail("incomplete live certification blockers must be explicit");
}

if (failures.length > 0) {
  console.error(failures.map((message) => `- ${message}`).join("\n"));
  process.exitCode = 1;
} else {
  console.log("OpenShell development profile is internally consistent.");
}
