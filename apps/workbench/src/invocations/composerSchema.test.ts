import assert from "node:assert/strict";
import { describe, it } from "vitest";
import { normalizeInvocationComposerSchema } from "./composerSchema";

const promptSchema = {
  additionalProperties: false,
  description: "A governed prompt contract.",
  properties: {
    prompt: {
      description: "What the agent should work on.",
      maxLength: 262_144,
      minLength: 1,
      title: "Prompt",
      type: "string",
    },
  },
  required: ["prompt"],
  title: "Agent prompt",
  type: "object",
};

describe("normalizeInvocationComposerSchema", () => {
  it("normalizes the closed prompt contract into bounded presentation fields", () => {
    const result = normalizeInvocationComposerSchema(promptSchema);

    assert.deepEqual(result, {
      ok: true,
      schema: {
        description: "A governed prompt contract.",
        fields: [
          {
            description: "What the agent should work on.",
            key: "prompt",
            label: "Prompt",
            maxLength: 262_144,
            minLength: 1,
            required: true,
          },
        ],
        title: "Agent prompt",
      },
    });
  });

  it("rejects open objects, references, unsupported types, and unbounded strings", () => {
    for (const schema of [
      { ...promptSchema, additionalProperties: true },
      { ...promptSchema, $ref: "https://example.invalid/schema" },
      {
        ...promptSchema,
        properties: { prompt: { maxLength: 64, type: "array" } },
      },
      {
        ...promptSchema,
        properties: { prompt: { type: "string" } },
      },
    ]) {
      assert.equal(normalizeInvocationComposerSchema(schema).ok, false);
    }
  });

  it("rejects ambiguous required fields and excessive form breadth", () => {
    assert.equal(
      normalizeInvocationComposerSchema({ ...promptSchema, required: ["missing"] }).ok,
      false,
    );
    assert.equal(
      normalizeInvocationComposerSchema({
        ...promptSchema,
        properties: Object.fromEntries(
          Array.from({ length: 9 }, (_, index) => [
            `field${index}`,
            { maxLength: 64, type: "string" },
          ]),
        ),
      }).ok,
      false,
    );
  });
});
