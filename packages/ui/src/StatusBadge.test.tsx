import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import { StatusBadge } from "./StatusBadge";

describe("StatusBadge", () => {
  it("renders text in addition to the decorative status marker", () => {
    const markup = renderToStaticMarkup(
      <StatusBadge tone="waiting">Decision required</StatusBadge>,
    );

    assert.match(markup, /data-tone="waiting"/u);
    assert.match(markup, /aria-hidden="true"/u);
    assert.match(markup, /Decision required/u);
  });
});
