import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import {
  normalizeInvocationComposerSchema,
  prepareInvocationInput,
} from "../invocations/composerSchema";
import { createPromptInputSchema, readPromptInputSchema } from "./promptInput";
import { RevisionInputEditor } from "./RevisionInputEditor";
import { validateServiceRevisionDraft } from "./ServiceRevisionDraftWorkflow";

const definition = { label: "Task", guidance: "Describe the requested change." };

describe("guided prompt input", () => {
  it("creates a revision contract that the invocation form can render and validate", () => {
    const value = createPromptInputSchema(definition);
    const draft = validateServiceRevisionDraft({
      inputSchema: value,
      outputSchema: "",
      requiredCapabilities: "",
      runtimeProfile: "reference/v1",
    });
    assert.deepEqual(draft.errors, {});
    assert.deepEqual(readPromptInputSchema(value), definition);
    const composer = normalizeInvocationComposerSchema(draft.request?.inputSchema);
    assert.equal(composer.ok, true);
    if (!composer.ok) return;
    assert.equal(composer.schema.fields[0]?.label, "Task");
    assert.equal(composer.schema.fields[0]?.required, true);
    const valid = prepareInvocationInput(
      {
        prompt: "Review this change",
      },
      composer.schema,
    );
    assert.deepEqual(valid.errors, {});
    assert.deepEqual(valid.input, { prompt: "Review this change" });
    assert.ok(prepareInvocationInput({ prompt: "" }, composer.schema).errors.prompt);
  });

  it("omits blank annotations without weakening the required prompt constraint", () => {
    const value = createPromptInputSchema({ label: "", guidance: " " });
    assert.deepEqual(readPromptInputSchema(value), { label: "", guidance: "" });
    assert.equal(normalizeInvocationComposerSchema(JSON.parse(value)).ok, true);
    assert.deepEqual(JSON.parse(value).required, ["prompt"]);
  });

  it("keeps custom semantics and malformed definitions out of guided mode", () => {
    const original = JSON.parse(createPromptInputSchema(definition));
    for (const altered of [
      { ...original, additionalProperties: true },
      { ...original, required: [] },
      { ...original, title: "Custom root annotation" },
      { ...original, $ref: "#/$defs/custom" },
      { ...original, properties: { ...original.properties, extra: { type: "boolean" } } },
      { ...original, properties: { prompt: { ...original.properties.prompt, pattern: "^safe$" } } },
      { ...original, properties: { prompt: { ...original.properties.prompt, maxLength: 10 } } },
      {
        ...original,
        properties: { prompt: { ...original.properties.prompt, title: "x".repeat(129) } },
      },
    ])
      assert.equal(readPromptInputSchema(JSON.stringify(altered)), undefined);
    for (const invalid of ["{", "null", "[]", "", "x".repeat((1 << 20) + 1)])
      assert.equal(readPromptInputSchema(invalid), undefined);
  });

  it("requires an explicit template choice and preserves custom input in JSON mode", () => {
    const blank = renderToStaticMarkup(
      <RevisionInputEditor disabled={false} value="" onChange={() => undefined} />,
    );
    assert.match(blank, /Use prompt input/u);
    assert.match(blank, /No input contract has been selected/u);
    const custom = '{"properties":{"prompt":{"pattern":"^original$"}}}';
    const markup = renderToStaticMarkup(
      <RevisionInputEditor disabled={false} value={custom} onChange={() => undefined} />,
    );
    assert.match(markup, /\^original\$/u);
    assert.doesNotMatch(markup, /Use prompt input|Use guided editor/u);
  });

  it("disables editing when authority or retry state forbids changes", () => {
    const markup = renderToStaticMarkup(
      <RevisionInputEditor
        disabled
        value={createPromptInputSchema(definition)}
        onChange={() => undefined}
      />,
    );
    const fields = markup.match(/<(?:input|textarea|button)\b[^>]*>/gu) ?? [];
    assert.equal(fields.length, 3);
    for (const field of fields) assert.match(field, /disabled/u);
  });
});
