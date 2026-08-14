import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import {
  createInvocationIdempotencyKey,
  InvocationComposerWorkflow,
  invocationTargetKey,
  validateInvocationComposerValues,
} from "./InvocationComposerWorkflow";

const schema = {
  fields: [{ key: "prompt", label: "Prompt", maxLength: 12, minLength: 1, required: true }],
};

describe("InvocationComposerWorkflow", () => {
  it("creates stable scoped request identifiers", () => {
    assert.equal(
      createInvocationIdempotencyKey(() => "00000000-0000-4000-8000-000000000001"),
      "invoke:00000000000040008000000000000001",
    );
    assert.equal(
      invocationTargetKey({ isolationDomainId: "iso_one", serviceId: "svc_one" }),
      "iso_one:svc_one",
    );
  });

  it("validates aliases and UTF-8 input bounds before transport", () => {
    assert.deepEqual(validateInvocationComposerValues("INVALID", { prompt: "" }, schema), {
      alias: "Use lowercase letters, numbers, and internal hyphens.",
      prompt: "Prompt is required.",
    });
    assert.deepEqual(validateInvocationComposerValues("stable", { prompt: "😀😀😀😀" }, schema), {
      prompt: "Prompt is too long.",
    });
    assert.deepEqual(validateInvocationComposerValues("stable", { prompt: "valid" }, schema), {});
  });

  it("renders observer authority and scope without enabling submission", () => {
    const markup = renderToStaticMarkup(
      <InvocationComposerWorkflow
        canInvoke={false}
        client={{} as DataGroundClient}
        disabledReason="Only service operators may invoke."
        inputSchema={{
          additionalProperties: false,
          properties: { prompt: { maxLength: 262_144, minLength: 1, type: "string" } },
          required: ["prompt"],
          type: "object",
        }}
        target={{
          isolationDomainId: "iso_00000000000000000001",
          serviceId: "svc_00000000000000000001",
        }}
      />,
    );

    assert.match(markup, /Observer access only/u);
    assert.match(markup, /iso_00000000000000000001/u);
    assert.match(markup, /svc_00000000000000000001/u);
    assert.match(markup, /disabled/u);
  });
});
