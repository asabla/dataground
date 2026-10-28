import assert from "node:assert/strict";
import { describe, it } from "vitest";
import { prepareContractPreview } from "./contractReview";

describe("contract review content", () => {
  it("distinguishes absence from an empty declared schema and preserves exact JSON values", () => {
    assert.deepEqual(prepareContractPreview(undefined), { state: "absent" });
    assert.deepEqual(prepareContractPreview({}), { state: "ready", text: "{}" });
    const schema = {
      type: "object",
      title: "<script>untrusted</script>",
      additionalProperties: false,
    };
    const preview = prepareContractPreview(schema);
    assert.equal(preview.state, "ready");
    if (preview.state === "ready") assert.deepEqual(JSON.parse(preview.text), schema);
  });

  it("uses compact JSON when formatting exceeds the display bound, without truncation", () => {
    const schema = { enum: Array(150000).fill("x") };
    const preview = prepareContractPreview(schema);
    assert.equal(preview.state, "ready");
    if (preview.state !== "ready") return;
    assert.equal(preview.text, JSON.stringify(schema));
    assert.deepEqual(JSON.parse(preview.text), schema);
  });

  it("rejects byte-oversized and non-serializable content without leaking it in diagnostics", () => {
    assert.deepEqual(prepareContractPreview({ description: "漢".repeat(400000) }), {
      state: "unavailable",
    });
    const cyclic: Record<string, unknown> = {};
    cyclic.self = cyclic;
    assert.deepEqual(prepareContractPreview(cyclic), { state: "unavailable" });
  });
});
