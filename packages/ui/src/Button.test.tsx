import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import { Button } from "./Button";

describe("Button", () => {
  it("renders native button semantics and its visual intent", () => {
    const markup = renderToStaticMarkup(<Button variant="primary">Create revision</Button>);

    assert.match(markup, /^<button/u);
    assert.match(markup, /data-variant="primary"/u);
    assert.match(markup, />Create revision<\/button>$/u);
  });

  it("preserves native disabled semantics", () => {
    const markup = renderToStaticMarkup(<Button isDisabled>Unavailable</Button>);

    assert.match(markup, /disabled=""/u);
    assert.match(markup, /data-disabled="true"/u);
  });
});
