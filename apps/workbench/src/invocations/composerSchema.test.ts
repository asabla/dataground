import assert from "node:assert/strict";
import { describe, it } from "vitest";
import { normalizeInvocationComposerSchema, prepareInvocationInput } from "./composerSchema";

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
            type: "string",
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

describe("typed invocation inputs", () => {
  const contract = {
    type: "object",
    additionalProperties: false,
    required: ["count", "enabled", "mode", "name"],
    properties: {
      count: { type: "integer", minimum: 0, maximum: 5 },
      ratio: { type: "number", minimum: -1, maximum: 1 },
      enabled: { type: "boolean" },
      mode: { type: "string", enum: ["", "brief", "full"] },
      name: { type: "string", minLength: 1, maxLength: 2 },
      optional: { type: "string", minLength: 3, maxLength: 10 },
      alias: { type: "string", minLength: 1, maxLength: 20 },
    },
  };
  function normalized() {
    const result = normalizeInvocationComposerSchema(contract);
    assert.ok(result.ok);
    return result.schema;
  }
  const values = {
    count: "0",
    enabled: "false",
    mode: "0",
    name: "😀é",
    optional: "",
    ratio: "-0.5",
  };

  it("preserves typed zero, false, and explicit empty enum values while omitting optional blanks", () => {
    assert.deepEqual(prepareInvocationInput(values, normalized()), {
      errors: {},
      input: { count: 0, enabled: false, mode: "", name: "😀é", ratio: -0.5 },
    });
  });

  it("rejects malformed numbers, unsafe precision, invalid choices, missing required values, and bounds", () => {
    for (const [key, raw] of [
      ["count", ""],
      ["count", "1.5"],
      ["count", "01"],
      ["count", "0x1"],
      ["count", "6"],
      ["count", "9007199254740993"],
      ["ratio", "NaN"],
      ["ratio", "1e999"],
      ["ratio", " 1 "],
      ["enabled", ""],
      ["enabled", "no"],
      ["mode", "brief"],
      ["mode", "99"],
      ["name", "😀éx"],
    ] as const) {
      assert.ok(
        prepareInvocationInput({ ...values, [key]: raw }, normalized()).errors[key],
        `${key}: ${raw}`,
      );
    }
  });

  it("does not infer constraints that the composer cannot represent", () => {
    for (const property of [
      { type: "number", maximum: Number.POSITIVE_INFINITY },
      { type: "number", minimum: 2, maximum: 1 },
      { type: "integer", multipleOf: 2 },
      { type: "boolean", enum: [true] },
      { type: "string", enum: ["x", "x"] },
      { type: "string", enum: [1, "x"] },
    ]) {
      assert.equal(
        normalizeInvocationComposerSchema({
          type: "object",
          additionalProperties: false,
          properties: { value: property },
        }).ok,
        false,
      );
    }
    assert.equal(
      normalizeInvocationComposerSchema({
        type: "object",
        additionalProperties: false,
        properties: { value: { type: "boolean" } },
        required: ["constructor"],
      }).ok,
      false,
    );
  });
});

it("does not read inherited object properties as provided input", () => {
  const result = normalizeInvocationComposerSchema({
    type: "object",
    additionalProperties: false,
    properties: { constructor: { type: "string", maxLength: 20 } },
  });
  assert.equal(result.ok, true);
  if (result.ok)
    assert.deepEqual(prepareInvocationInput({}, result.schema), { errors: {}, input: {} });
});
