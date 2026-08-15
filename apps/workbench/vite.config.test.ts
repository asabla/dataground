import assert from "node:assert/strict";
import { describe, it } from "vitest";
import config from "./vite.config";

describe("Workbench development proxy", () => {
  it("targets the dedicated loopback API listener", () => {
    assert.deepEqual(config.server?.proxy?.["/v1"], {
      target: "http://127.0.0.1:8082",
    });
  });
});
