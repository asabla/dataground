import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import {
  AgentServiceCreateWorkflow,
  createServiceIdempotencyKey,
  validateAgentServiceCreateRequest,
} from "./AgentServiceCreateWorkflow";

describe("AgentServiceCreateWorkflow", () => {
  it("creates stable command identifiers", () => {
    assert.equal(
      createServiceIdempotencyKey(() => "00000000-0000-4000-8000-000000000001"),
      "service:00000000000040008000000000000001",
    );
  });

  it("validates normalized names, UTF-8 bounds, and unsupported characters", () => {
    assert.deepEqual(validateAgentServiceCreateRequest("   ", ""), {
      name: "Service name is required.",
    });
    assert.deepEqual(validateAgentServiceCreateRequest("Valid", "😀".repeat(513)), {
      description: "Description must not exceed 2,048 bytes.",
    });
    assert.deepEqual(validateAgentServiceCreateRequest("Valid\0", ""), {
      name: "Service name contains unsupported characters.",
    });
    assert.deepEqual(validateAgentServiceCreateRequest("Valid", "Optional context"), {});
  });

  it("renders observer scope without enabling creation", () => {
    const markup = renderToStaticMarkup(
      <AgentServiceCreateWorkflow
        canCreate={false}
        client={{} as DataGroundClient}
        disabledReason="Only service operators may create services."
        isolationDomainId="iso_00000000000000000001"
      />,
    );

    assert.match(markup, /Observer access only/u);
    assert.match(markup, /iso_00000000000000000001/u);
    assert.match(markup, /disabled/u);
  });
});
