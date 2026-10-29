import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { it } from "vitest";
import { Tabs } from "./Tabs";

it("links named tabs and panels without mounting unvisited contents", () => {
  const markup = renderToStaticMarkup(
    <Tabs
      label="Service views"
      selectedId="overview"
      onSelectionChange={() => {}}
      items={[
        { id: "overview", label: "Overview", content: <p>Service overview</p> },
        { id: "build", label: "Build", content: <p>Private form contents</p> },
      ]}
    />,
  );
  assert.match(markup, /(?=[^>]*role="tablist")(?=[^>]*aria-label="Service views")/u);
  assert.match(markup, /role="tab"/u);
  assert.match(markup, /role="tabpanel"/u);
  assert.match(markup, /aria-controls=/u);
  assert.match(markup, /Service overview/u);
  assert.doesNotMatch(markup, /Private form contents/u);
});
