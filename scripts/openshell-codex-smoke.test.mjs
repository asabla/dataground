import assert from "node:assert/strict";
import { chmodSync, mkdtempSync, realpathSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import {
  parseCodexCredentials,
  readCodexCredentials,
  validateBridgeProfile,
  validateBridgeProvider,
} from "./openshell-codex-smoke.mjs";

function jwt(expiry) {
  return [
    Buffer.from('{"alg":"none"}').toString("base64url"),
    Buffer.from(JSON.stringify({ exp: expiry })).toString("base64url"),
    "signature",
  ].join(".");
}

function authDocument(expiry) {
  return {
    auth_mode: "chatgpt",
    tokens: {
      access_token: jwt(expiry),
      account_id: "account-test",
      id_token: "id-token",
      refresh_token: "refresh-token",
    },
  };
}

function bridgeProfile() {
  return {
    id: "dataground-codex-chatgpt",
    resource_version: 1,
    display_name: "DataGround Codex ChatGPT bridge",
    description: "Loopback development profile for OpenShell-mediated Codex ChatGPT OAuth.",
    category: "agent",
    credentials: [
      {
        name: "access_token",
        description: "Short-lived Codex OAuth access token",
        env_vars: ["OPENAI_API_KEY"],
        required: true,
        auth_style: "",
        header_name: "",
        query_param: "",
      },
      {
        name: "account_id",
        description: "Codex ChatGPT account identifier",
        env_vars: ["CHATGPT_ACCOUNT_ID"],
        required: true,
        auth_style: "",
        header_name: "",
        query_param: "",
      },
    ],
    endpoints: ["api.openai.com", "auth.openai.com", "chatgpt.com", "ab.chatgpt.com"].map(
      (host) => ({
        host,
        port: 443,
        protocol: "rest",
        access: "read-write",
        enforcement: "enforce",
      }),
    ),
    binaries: ["/usr/bin/codex", "/usr/local/bin/codex", "/usr/lib/node_modules/@openai/**"],
    inference_capable: true,
    discovery: { credentials: ["access_token", "account_id"] },
  };
}

test("accepts a current complete Codex ChatGPT credential document", () => {
  const now = Date.UTC(2026, 7, 15, 12);
  const credentials = parseCodexCredentials(
    Buffer.from(JSON.stringify(authDocument(Math.floor(now / 1000) + 3600))),
    now,
  );
  assert.equal(credentials.accountID, "account-test");
  assert.match(credentials.accessToken, /^[^.]+\.[^.]+\.[^.]+$/u);
});

test("rejects an expired Codex ChatGPT access token", () => {
  const now = Date.UTC(2026, 7, 15, 12);
  assert.throws(
    () =>
      parseCodexCredentials(
        Buffer.from(JSON.stringify(authDocument(Math.floor(now / 1000) - 1))),
        now,
      ),
    /expired or too close to expiry/u,
  );
});

test("reads only an owner-only direct Codex authentication file", (t) => {
  const root = realpathSync(mkdtempSync(join(tmpdir(), "dataground-codex-smoke-test-")));
  chmodSync(root, 0o700);
  t.after(() => rmSync(root, { force: true, recursive: true }));
  const now = Date.UTC(2026, 7, 15, 12);
  const authPath = join(root, "auth.json");
  writeFileSync(authPath, JSON.stringify(authDocument(Math.floor(now / 1000) + 3600)), {
    mode: 0o600,
  });
  assert.equal(readCodexCredentials(authPath, now).accountID, "account-test");

  const linkPath = join(root, "linked-auth.json");
  symlinkSync(authPath, linkPath);
  assert.throws(() => readCodexCredentials(linkPath, now), /unsafe/u);
});

test("accepts only the exact normalized bridge profile and provider metadata", () => {
  const profile = bridgeProfile();
  assert.doesNotThrow(() => validateBridgeProfile(profile));
  assert.throws(
    () => validateBridgeProfile({ ...profile, endpoints: profile.endpoints.slice(1) }),
    /does not match/u,
  );

  const provider = {
    name: "dataground-codex-chatgpt",
    type: "dataground-codex-chatgpt",
    resource_version: 1,
    credential_keys: ["OPENAI_API_KEY", "CHATGPT_ACCOUNT_ID"],
  };
  assert.doesNotThrow(() => validateBridgeProvider(provider));
  assert.throws(
    () => validateBridgeProvider({ ...provider, credential_keys: ["OPENAI_API_KEY"] }),
    /does not match/u,
  );
});
