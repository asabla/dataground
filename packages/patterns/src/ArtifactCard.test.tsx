import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import { ArtifactCard, type ArtifactResource } from "./ArtifactCard";

const artifact: ArtifactResource = {
  digest: "sha256:1b93a4b13f9917ba7e33ebf29560b17d50593f23bc1dfeeec961ae0cfabcb9e6",
  invocationId: "inv_00000000000000000001",
  kind: "structured-output",
  mediaType: "application/json",
  metadata: {
    createdAt: "2026-08-14T12:00:00Z",
    createdBy: "reference-runtime",
    generation: 1,
    id: "art_00000000000000000001",
    isolationDomainId: "iso_00000000000000000001",
    provenance: {
      requestCorrelationId: "cor_00000000000000000001",
      sourceRevision: "rev_00000000000000000001",
    },
    updatedAt: "2026-08-14T12:00:00.001Z",
    version: 1,
  },
  name: "result.json",
  sensitive: true,
  sizeBytes: 2_097_152,
  state: "available",
};

const reference = {
  artifactId: artifact.metadata.id,
  invocationId: artifact.invocationId,
  isolationDomainId: artifact.metadata.isolationDomainId,
};

describe("ArtifactCard", () => {
  it("renders integrity, sensitivity, scope, and provenance without content controls", () => {
    const markup = renderToStaticMarkup(<ArtifactCard artifact={artifact} reference={reference} />);

    assert.match(markup, /Artifact available/u);
    assert.match(markup, /result.json/u);
    assert.match(markup, /2.00 MiB \(2,097,152 bytes\)/u);
    assert.match(markup, /sha256:1b93/u);
    assert.match(markup, /marked sensitive/u);
    assert.match(markup, /rev_00000000000000000001/u);
    assert.doesNotMatch(markup, /Download/u);
    assert.doesNotMatch(markup, /Open content/u);
  });

  it("retains confirmed metadata when refresh is degraded", () => {
    const markup = renderToStaticMarkup(
      <ArtifactCard
        artifact={artifact}
        error={{ message: "The artifact service is unavailable.", retryable: true }}
        onRefresh={() => undefined}
        reference={reference}
      />,
    );

    assert.match(markup, /Metadata degraded/u);
    assert.match(markup, /Artifact metadata not refreshed/u);
    assert.match(markup, /Retry metadata/u);
    assert.match(markup, /Content digest/u);
  });

  it("renders bounded loading and unavailable states", () => {
    const loading = renderToStaticMarkup(
      <ArtifactCard isLoading onRefresh={() => undefined} reference={reference} />,
    );
    const unavailable = renderToStaticMarkup(
      <ArtifactCard
        error={{ message: "Artifact metadata was not found.", retryable: false }}
        reference={reference}
      />,
    );

    assert.match(loading, /Loading metadata/u);
    assert.match(loading, /aria-live="polite"/u);
    assert.match(unavailable, /Metadata unavailable/u);
    assert.match(unavailable, /Artifact metadata unavailable/u);
    assert.match(unavailable, /role="alert"/u);
  });

  it("does not offer a retry for an initial non-retryable failure", () => {
    const markup = renderToStaticMarkup(
      <ArtifactCard
        error={{ message: "Artifact metadata is invalid.", retryable: false }}
        onRefresh={() => undefined}
        reference={reference}
      />,
    );

    assert.doesNotMatch(markup, /Refresh metadata/u);
    assert.doesNotMatch(markup, /Retry metadata/u);
  });

  it("shows unknown states and kinds without implying availability", () => {
    const markup = renderToStaticMarkup(
      <ArtifactCard
        artifact={{ ...artifact, kind: "future-kind", sensitive: false, state: "quarantined" }}
        reference={reference}
      />,
    );

    assert.match(markup, /Unknown state: quarantined/u);
    assert.match(markup, /Unknown: future-kind/u);
    assert.match(markup, /metadata only/u);
    assert.doesNotMatch(markup, /Artifact available/u);
  });

  it("neutralizes control characters in safe metadata text", () => {
    const markup = renderToStaticMarkup(
      <ArtifactCard
        artifact={{ ...artifact, name: "safe\u001b[31m.json" }}
        reference={reference}
      />,
    );

    assert.match(markup, /safe�\[31m.json/u);
  });

  it("uses explicit fallbacks for metadata that normalizes to empty text", () => {
    const markup = renderToStaticMarkup(
      <ArtifactCard
        artifact={{
          ...artifact,
          mediaType: "\t",
          metadata: { ...artifact.metadata, createdBy: " " },
          name: "\n",
        }}
        reference={reference}
      />,
    );

    assert.match(markup, /Unnamed artifact/u);
    assert.match(markup, /Not reported/u);
  });

  it("bounds error and reference text supplied by the product workflow", () => {
    const markup = renderToStaticMarkup(
      <ArtifactCard
        error={{ message: "e".repeat(700), retryable: false }}
        reference={{ ...reference, artifactId: `art_${"a".repeat(200)}` }}
      />,
    );

    assert.doesNotMatch(markup, /e{513}/u);
    assert.doesNotMatch(markup, /a{65}/u);
    assert.match(markup, /…/u);
  });
});
