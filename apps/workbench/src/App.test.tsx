import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import { App } from "./App";

describe("App", () => {
  it("renders an explicit bootstrap state in the main landmark", () => {
    const markup = renderToStaticMarkup(<App />);

    assert.match(markup, /<main/);
    assert.match(markup, /DataGround/);
    assert.match(markup, /Project foundation ready/);
    assert.match(markup, /Product resources remain unavailable/);
  });
});
