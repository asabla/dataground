import { spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import {
  accessSync,
  closeSync,
  constants,
  fstatSync,
  lstatSync,
  openSync,
  readFileSync,
  realpathSync,
} from "node:fs";
import { homedir } from "node:os";
import { dirname, isAbsolute, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import release from "../deploy/openshell/local/release.json" with { type: "json" };

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const gatewayEndpoint = "http://127.0.0.1:8080";
const gatewayWorkspace = "default";
const expectedOpenShellVersion = `openshell ${release.version}`;
const providerName = "dataground-codex-chatgpt";
const providerProfilePath = join(
  repositoryRoot,
  "deploy/openshell/local/providers/dataground-codex-chatgpt.yaml",
);
const sandboxPolicyPath = join(repositoryRoot, "deploy/openshell/policies/deny-all.yaml");
const expectedCredentialKeys = ["CHATGPT_ACCOUNT_ID", "OPENAI_API_KEY"];
const sentinel = "dataground-openshell-e2e-ok";
const modelProviderConfig =
  'model_providers.dataground={name="DataGround OpenShell",base_url="https://chatgpt.com/backend-api/codex",env_key="OPENAI_API_KEY",env_http_headers={"ChatGPT-Account-Id"="CHATGPT_ACCOUNT_ID"},wire_api="responses",requires_openai_auth=false,supports_websockets=false}';
const maximumCommandOutputBytes = 4 << 20;
const maximumAuthFileBytes = 64 << 10;

function fail(message) {
  throw new Error(message);
}

export function smokeModel(value) {
  if (typeof value !== "string" || !/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/u.test(value)) {
    fail("DATAGROUND_CODEX_SMOKE_MODEL must name an explicit model available to the local account");
  }
  return value;
}

function sameFile(left, right) {
  return (
    left.dev === right.dev &&
    left.ino === right.ino &&
    left.mode === right.mode &&
    left.size === right.size &&
    left.mtimeMs === right.mtimeMs
  );
}

function safeOwner(info) {
  return typeof process.getuid !== "function" || info.uid === process.getuid();
}

function hasASCIIControlCharacter(value) {
  for (const character of value) {
    const codePoint = character.codePointAt(0);
    if (codePoint <= 31 || codePoint === 127) {
      return true;
    }
  }
  return false;
}

function command(binary, args, options = {}) {
  const result = spawnSync(binary, args, {
    encoding: null,
    env: options.env ?? process.env,
    maxBuffer: maximumCommandOutputBytes,
    shell: false,
    stdio: ["ignore", "pipe", "pipe"],
  });
  if (result.error) {
    fail("required local command could not be executed");
  }
  const stdout = Buffer.isBuffer(result.stdout) ? result.stdout : Buffer.alloc(0);
  const stderr = Buffer.isBuffer(result.stderr) ? result.stderr : Buffer.alloc(0);
  return { status: result.status ?? -1, stderr, stdout };
}

function clearResult(result) {
  result.stdout.fill(0);
  result.stderr.fill(0);
}

function gatewayArguments(...args) {
  return ["--gateway-endpoint", gatewayEndpoint, "--workspace", gatewayWorkspace, ...args];
}

function successfulJSON(binary, args) {
  const result = command(binary, args);
  try {
    if (result.status !== 0 || result.stdout.length === 0) {
      fail("OpenShell returned an invalid local response");
    }
    return JSON.parse(result.stdout.toString("utf8"));
  } catch (error) {
    if (error?.message?.startsWith("OpenShell ")) {
      throw error;
    }
    fail("OpenShell returned malformed local JSON");
  } finally {
    clearResult(result);
  }
}

export function validateBridgeProfile(profile) {
  const expectedCredentials = [
    { env: "OPENAI_API_KEY", name: "access_token" },
    { env: "CHATGPT_ACCOUNT_ID", name: "account_id" },
  ];
  const expectedEndpoints = ["ab.chatgpt.com", "api.openai.com", "auth.openai.com", "chatgpt.com"];
  const expectedBinaries = [
    "/usr/bin/codex",
    "/usr/lib/node_modules/@openai/**",
    "/usr/local/bin/codex",
  ];
  if (
    profile?.id !== providerName ||
    profile?.category !== "agent" ||
    profile?.inference_capable !== true ||
    !Number.isSafeInteger(profile?.resource_version) ||
    profile.resource_version < 1 ||
    JSON.stringify(
      (profile.credentials ?? [])
        .map((credential) => ({
          env: credential.env_vars?.length === 1 ? credential.env_vars[0] : "",
          name: credential.name,
        }))
        .sort((left, right) => left.name.localeCompare(right.name)),
    ) !==
      JSON.stringify(
        expectedCredentials.sort((left, right) => left.name.localeCompare(right.name)),
      ) ||
    (profile.credentials ?? []).some(
      (credential) =>
        credential.required !== true ||
        credential.auth_style !== "" ||
        credential.header_name !== "" ||
        credential.query_param !== "",
    ) ||
    JSON.stringify([...(profile.discovery?.credentials ?? [])].sort()) !==
      JSON.stringify(["access_token", "account_id"]) ||
    JSON.stringify((profile.endpoints ?? []).map((endpoint) => endpoint.host).sort()) !==
      JSON.stringify(expectedEndpoints) ||
    (profile.endpoints ?? []).some(
      (endpoint) =>
        endpoint.port !== 443 ||
        endpoint.protocol !== "rest" ||
        endpoint.access !== "read-write" ||
        endpoint.enforcement !== "enforce",
    ) ||
    JSON.stringify([...(profile.binaries ?? [])].sort()) !== JSON.stringify(expectedBinaries)
  ) {
    fail("the installed OpenShell Codex bridge profile does not match the checked profile");
  }
}

export function validateBridgeProvider(provider) {
  if (
    provider?.name !== providerName ||
    provider?.type !== providerName ||
    !Number.isSafeInteger(provider?.resource_version) ||
    provider.resource_version < 1 ||
    JSON.stringify([...(provider.credential_keys ?? [])].sort()) !==
      JSON.stringify(expectedCredentialKeys)
  ) {
    fail("the installed OpenShell Codex bridge provider does not match the checked profile");
  }
}

function resolveOpenShellBinary() {
  for (const directory of (process.env.PATH ?? "").split(":")) {
    if (!isAbsolute(directory)) {
      continue;
    }
    const candidate = join(directory, "openshell");
    try {
      accessSync(candidate, constants.X_OK);
      const resolved = realpathSync(candidate);
      const info = lstatSync(resolved);
      if (info.isFile() && (info.uid === 0 || safeOwner(info)) && (info.mode & 0o022) === 0) {
        return resolved;
      }
    } catch {
      // Continue to the next explicit PATH entry.
    }
  }
  fail("a non-writable OpenShell executable is required on PATH");
}

function requirePinnedOpenShell(binary) {
  const result = command(binary, ["--version"]);
  try {
    if (result.status !== 0 || result.stdout.toString("utf8").trim() !== expectedOpenShellVersion) {
      fail(`OpenShell ${release.version} is required for the local Codex smoke flow`);
    }
  } finally {
    clearResult(result);
  }
}

function requireGateway(binary) {
  const status = successfulJSON(binary, gatewayArguments("status", "--output", "json"));
  if (
    status?.status !== "connected" ||
    status.server !== gatewayEndpoint ||
    status.version !== release.version
  ) {
    fail("the pinned local OpenShell gateway is not ready");
  }
}

function ensureBridgeProfile(binary) {
  let exported = command(
    binary,
    gatewayArguments("provider", "profile", "export", providerName, "--output", "json"),
  );
  if (exported.status !== 0) {
    clearResult(exported);
    const lint = command(
      binary,
      gatewayArguments("provider", "profile", "lint", "--file", providerProfilePath),
    );
    try {
      if (lint.status !== 0) {
        fail("the checked OpenShell Codex bridge profile did not lint");
      }
    } finally {
      clearResult(lint);
    }
    const imported = command(
      binary,
      gatewayArguments("provider", "profile", "import", "--file", providerProfilePath),
    );
    try {
      if (imported.status !== 0) {
        fail("the checked OpenShell Codex bridge profile could not be installed");
      }
    } finally {
      clearResult(imported);
    }
    exported = command(
      binary,
      gatewayArguments("provider", "profile", "export", providerName, "--output", "json"),
    );
  }
  try {
    if (exported.status !== 0) {
      fail("the installed OpenShell Codex bridge profile could not be observed");
    }
    validateBridgeProfile(JSON.parse(exported.stdout.toString("utf8")));
  } catch (error) {
    if (error?.message?.startsWith("the installed ")) {
      throw error;
    }
    fail("the installed OpenShell Codex bridge profile is malformed");
  } finally {
    clearResult(exported);
  }
}

export function readWorkspaceResources(readPage, collection) {
  const resources = [];
  const names = new Set();
  const tokens = new Set();
  let token = "";
  for (let page = 0; page < 16; page += 1) {
    const value = readPage(token);
    const rows = value?.[collection];
    if (!Array.isArray(rows) || rows.length > 100) {
      fail("OpenShell returned an invalid workspace list");
    }
    for (const row of rows) {
      if (
        row?.workspace !== gatewayWorkspace ||
        typeof row.name !== "string" ||
        row.name.length === 0 ||
        names.has(row.name)
      ) {
        fail("OpenShell returned an invalid workspace resource");
      }
      names.add(row.name);
      resources.push(row);
    }
    token = value.next_page_token;
    if (token === "") {
      return resources;
    }
    if (
      typeof token !== "string" ||
      token.length > 4096 ||
      hasASCIIControlCharacter(token) ||
      tokens.has(token)
    ) {
      fail("OpenShell returned an invalid workspace page token");
    }
    tokens.add(token);
  }
  fail("OpenShell workspace list exceeded the page limit");
}

function workspaceResources(binary, resource, collection) {
  return readWorkspaceResources((token) => {
    const args = [resource, "list", "--output", "json", "--page-size", "100"];
    if (token) {
      args.push(`--page-token=${token}`);
    }
    return successfulJSON(binary, gatewayArguments(...args));
  }, collection);
}

function defaultAuthPath() {
  const codexHome = process.env.CODEX_HOME || join(homedir(), ".codex");
  if (!isAbsolute(codexHome)) {
    fail("CODEX_HOME must be absolute when set");
  }
  return join(codexHome, "auth.json");
}

function credentialChild(binary, action, authPath) {
  const child = spawnSync(process.execPath, [fileURLToPath(import.meta.url), action], {
    encoding: null,
    env: {
      DATAGROUND_CODEX_AUTH_FILE: authPath,
      DATAGROUND_OPENSHELL_BINARY: binary,
    },
    maxBuffer: maximumCommandOutputBytes,
    shell: false,
    stdio: ["ignore", "pipe", "pipe"],
  });
  const stdout = Buffer.isBuffer(child.stdout) ? child.stdout : Buffer.alloc(0);
  const stderr = Buffer.isBuffer(child.stderr) ? child.stderr : Buffer.alloc(0);
  try {
    if (child.error || child.status !== 0) {
      fail("OpenShell could not import the local Codex credentials");
    }
  } finally {
    stdout.fill(0);
    stderr.fill(0);
  }
}

function ensureBridgeProvider(binary) {
  const before = workspaceResources(binary, "provider", "providers");
  const existing = before.find((provider) => provider.name === providerName);
  if (existing) {
    validateBridgeProvider(existing);
  }
  credentialChild(
    binary,
    existing ? "credential-child-update" : "credential-child-create",
    defaultAuthPath(),
  );
  const after = workspaceResources(binary, "provider", "providers").find(
    (provider) => provider.name === providerName,
  );
  validateBridgeProvider(after);
}

export function parseCodexCredentials(content, now = Date.now()) {
  let document;
  let text = "";
  try {
    text = content.toString("utf8");
    document = JSON.parse(text);
  } catch {
    fail("the local Codex authentication file is malformed");
  } finally {
    text = "";
  }
  const accessToken = document?.tokens?.access_token;
  const accountID = document?.tokens?.account_id;
  if (
    document?.auth_mode !== "chatgpt" ||
    typeof accessToken !== "string" ||
    accessToken.length < 1 ||
    accessToken.length > maximumAuthFileBytes ||
    typeof accountID !== "string" ||
    accountID.length < 1 ||
    accountID.length > 1024 ||
    hasASCIIControlCharacter(accountID)
  ) {
    fail("a complete local Codex ChatGPT authentication is required");
  }
  try {
    const parts = accessToken.split(".");
    const claims = parts.length === 3 ? JSON.parse(Buffer.from(parts[1], "base64url")) : null;
    if (!Number.isSafeInteger(claims?.exp) || claims.exp * 1000 <= now + 5 * 60 * 1000) {
      fail("the local Codex access token is expired or too close to expiry");
    }
  } catch (error) {
    if (error?.message?.startsWith("the local Codex access token")) {
      throw error;
    }
    fail("the local Codex access token is malformed");
  }
  if (document.tokens) {
    document.tokens.access_token = "";
    document.tokens.refresh_token = "";
    document.tokens.id_token = "";
    document.tokens.account_id = "";
  }
  return { accessToken, accountID };
}

export function readCodexCredentials(path, now = Date.now()) {
  if (!isAbsolute(path) || typeof constants.O_NOFOLLOW !== "number") {
    fail("a no-follow absolute Codex authentication path is required");
  }
  const cleanPath = resolve(path);
  const parentPath = dirname(cleanPath);
  const parent = lstatSync(parentPath);
  if (
    parent.isSymbolicLink() ||
    !parent.isDirectory() ||
    !safeOwner(parent) ||
    (parent.mode & 0o022) !== 0 ||
    realpathSync(parentPath) !== parentPath
  ) {
    fail("the local Codex authentication directory is unsafe");
  }
  const before = lstatSync(cleanPath);
  if (
    before.isSymbolicLink() ||
    !before.isFile() ||
    !safeOwner(before) ||
    (before.mode & 0o777) !== 0o600 ||
    before.size < 1 ||
    before.size > maximumAuthFileBytes ||
    realpathSync(cleanPath) !== cleanPath
  ) {
    fail("the local Codex authentication file is unsafe");
  }
  const descriptor = openSync(cleanPath, constants.O_RDONLY | constants.O_NOFOLLOW);
  let content;
  try {
    const opened = fstatSync(descriptor);
    if (!sameFile(before, opened)) {
      fail("the local Codex authentication file changed while opening");
    }
    content = readFileSync(descriptor);
  } finally {
    closeSync(descriptor);
  }
  try {
    const after = lstatSync(cleanPath);
    if (!sameFile(before, after)) {
      fail("the local Codex authentication file changed while reading");
    }
    return parseCodexCredentials(content, now);
  } finally {
    content.fill(0);
  }
}

function runCredentialChild(action) {
  const binary = process.env.DATAGROUND_OPENSHELL_BINARY;
  const authPath = process.env.DATAGROUND_CODEX_AUTH_FILE;
  if (!binary || !isAbsolute(binary) || !authPath || !isAbsolute(authPath)) {
    fail("the credential child configuration is invalid");
  }
  const credentials = readCodexCredentials(authPath);
  const providerArgs =
    action === "credential-child-create"
      ? ["provider", "create", "--name", providerName, "--type", providerName]
      : ["provider", "update", providerName];
  const args = gatewayArguments(
    ...providerArgs,
    "--credential",
    "OPENAI_API_KEY",
    "--credential",
    "CHATGPT_ACCOUNT_ID",
  );
  const result = command(binary, args, {
    env: {
      CHATGPT_ACCOUNT_ID: credentials.accountID,
      OPENAI_API_KEY: credentials.accessToken,
    },
  });
  credentials.accessToken = "";
  credentials.accountID = "";
  try {
    if (result.status !== 0) {
      fail("OpenShell credential import failed");
    }
  } finally {
    clearResult(result);
  }
}

function sandboxNames(binary) {
  return workspaceResources(binary, "sandbox", "sandboxes").map((sandbox) => sandbox.name);
}

function runSmoke(binary, model) {
  // OpenShell 0.1.9 reserves only 19 characters for a routable sandbox name.
  const sandboxName = `dg-${randomBytes(8).toString("hex")}`;
  if (sandboxNames(binary).includes(sandboxName)) {
    fail("the fresh OpenShell smoke sandbox name already exists");
  }
  let result;
  let outcome;
  try {
    result = command(
      binary,
      gatewayArguments(
        "sandbox",
        "create",
        "--name",
        sandboxName,
        "--from",
        release.images.workload,
        "--policy",
        sandboxPolicyPath,
        "--provider",
        providerName,
        "--no-auto-providers",
        "--approval-mode",
        "manual",
        "--no-tty",
        "--no-keep",
        "--",
        "codex",
        "exec",
        "-c",
        'model_provider="dataground"',
        "-c",
        modelProviderConfig,
        "--model",
        model,
        "--ephemeral",
        "--skip-git-repo-check",
        "--sandbox",
        "read-only",
        "--color",
        "never",
        `Reply with exactly: ${sentinel}`,
      ),
    );
    const output = `${result.stdout.toString("utf8")}\n${result.stderr.toString("utf8")}`;
    const matched = output.split(/\r?\n/u).some((line) => line.trim() === sentinel);
    if (result.status !== 0 || !matched) {
      outcome = new Error("the real OpenShell Codex smoke turn did not complete exactly");
    }
  } catch (error) {
    outcome = error;
  } finally {
    if (result) {
      clearResult(result);
    }
    const remaining = sandboxNames(binary);
    if (remaining.includes(sandboxName)) {
      const deletion = command(binary, gatewayArguments("sandbox", "delete", sandboxName));
      const deletionFailed = deletion.status !== 0;
      clearResult(deletion);
      if (deletionFailed || sandboxNames(binary).includes(sandboxName)) {
        outcome = new Error("the OpenShell Codex smoke sandbox cleanup is uncertain");
      }
    }
  }
  if (outcome) {
    throw outcome;
  }
}

function main() {
  const model = smokeModel(process.env.DATAGROUND_CODEX_SMOKE_MODEL);
  const binary = resolveOpenShellBinary();
  requirePinnedOpenShell(binary);
  requireGateway(binary);
  ensureBridgeProfile(binary);
  ensureBridgeProvider(binary);
  runSmoke(binary, model);
  process.stdout.write("OpenShell Codex smoke completed successfully.\n");
}

const direct = process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1]);
if (direct) {
  try {
    const action = process.argv[2];
    if (action === "credential-child-create" || action === "credential-child-update") {
      runCredentialChild(action);
    } else if (action === undefined) {
      main();
    } else {
      fail("usage: node scripts/openshell-codex-smoke.mjs");
    }
  } catch (error) {
    console.error(`OpenShell Codex smoke failed: ${error.message}`);
    process.exitCode = 1;
  }
}
