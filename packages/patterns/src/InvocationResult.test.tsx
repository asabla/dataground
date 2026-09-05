import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import { InvocationResult } from "./InvocationResult";

describe("InvocationResult", () => {
  it("keeps content absent until explicitly supplied", () => {
    const markup = renderToStaticMarkup(<InvocationResult />);
    assert.match(markup, /Show result/);
    assert.doesNotMatch(markup, /<pre/);
  });
  it("renders untrusted result JSON as escaped text in a keyboard-scrollable region", () => {
    const markup = renderToStaticMarkup(
      <InvocationResult text={'{"output":"<script>alert(1)</script>"}'} />,
    );
    assert.match(markup, /&lt;script&gt;/);
    assert.doesNotMatch(markup, /<script>/);
    assert.match(markup, /aria-label="Invocation result JSON"/);
    assert.match(markup, /tabindex="0"/);
    assert.match(markup, /Hide result/);
  });
});
