import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { it } from "vitest";
import { CopyButton } from "./CopyButton";

it("names the explicit copy action without embedding its value in the control", () => {
  const markup = renderToStaticMarkup(
    <CopyButton label="resource identifier" value="exact-resource" />,
  );
  assert.match(markup, /aria-label="Copy resource identifier"/u);
  assert.match(markup, /role="status"/u);
  assert.doesNotMatch(markup, /exact-resource|copied\./u);
});
it("does not offer an empty or unavailable copy", () => {
  for (const props of [{ value: "" }, { value: "resource", isDisabled: true }]) {
    assert.match(renderToStaticMarkup(<CopyButton label="identifier" {...props} />), /disabled/u);
  }
});
