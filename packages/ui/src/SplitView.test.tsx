import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { it } from "vitest";
import { SplitView } from "./SplitView";

it("names the resizable pane and exposes bounded separator state", () => {
  const markup = renderToStaticMarkup(
    <SplitView primaryLabel="Run record" primary={<p>Run</p>} secondary={<p>Inspection</p>} />,
  );
  assert.match(markup, /<hr[^>]*aria-controls=/u);
  assert.match(markup, /aria-label="Run record"/u);
  assert.match(markup, /aria-valuenow="60"/u);
  assert.match(markup, /aria-valuemin="40"/u);
  assert.match(markup, /aria-valuemax="70"/u);
  assert.match(markup, /aria-orientation="vertical"/u);
  assert.match(markup, /tabindex="0"/u);
});
it("does not expose a resize control without a secondary pane", () => {
  const markup = renderToStaticMarkup(
    <SplitView primaryLabel="Run record" primary={<p>Run</p>} secondary={false} />,
  );
  assert.match(markup, /Run/u);
  assert.doesNotMatch(markup, /<hr|data-split|Inspection/u);
});
