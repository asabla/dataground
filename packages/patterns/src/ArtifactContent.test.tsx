import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import { ArtifactContent } from "./ArtifactContent";

describe("ArtifactContent", () => {
  it("requires an explicit read and exposes loading without moving focus", () => {
    const idle = renderToStaticMarkup(<ArtifactContent />);
    assert.match(idle, /Read content/u);
    assert.doesNotMatch(idle, /<a/u);
    const loading = renderToStaticMarkup(<ArtifactContent isLoading />);
    assert.match(loading, /role="status"/u);
    assert.match(loading, /Cancel content read/u);
  });
  it("escapes content and gives the verified download a distinct action", () => {
    const markup = renderToStaticMarkup(
      <ArtifactContent
        text="<script>private</script>"
        download={{ url: "blob:verified", filename: "artifact.bin" }}
      />,
    );
    assert.doesNotMatch(markup, /<script>/u);
    assert.match(markup, /&lt;script&gt;/u);
    assert.match(markup, /aria-label="Artifact text"/u);
    assert.match(markup, /download="artifact.bin"/u);
    assert.match(markup, /Hide content/u);
  });
  it("explains unavailable content without exposing an actionable read", () => {
    const markup = renderToStaticMarkup(
      <ArtifactContent unavailableMessage="Artifact is not available." />,
    );
    assert.doesNotMatch(markup, /<button/u);
    assert.match(markup, /not available/u);
  });
});
