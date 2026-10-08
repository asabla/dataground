import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import {
  ArtifactWorkflow,
  artifactReferenceKey,
  artifactWorkflowReducer,
} from "./ArtifactWorkflow";
import type { InvocationArtifact } from "./client";

const client = {} as DataGroundClient;
const reference = {
  artifactId: "art_00000000000000000001",
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
};
const referenceKey = artifactReferenceKey(reference);

const artifact: InvocationArtifact = {
  digest: "sha256:1b93a4b13f9917ba7e33ebf29560b17d50593f23bc1dfeeec961ae0cfabcb9e6",
  invocationId: reference.invocationId,
  kind: "structured-output",
  mediaType: "application/json",
  metadata: {
    createdAt: "2026-08-14T12:00:00Z",
    createdBy: "reference-runtime",
    generation: 1,
    id: reference.artifactId,
    isolationDomainId: reference.isolationDomainId,
    updatedAt: "2026-08-14T12:00:00.001Z",
    version: 1,
  },
  name: "result.json",
  sensitive: true,
  sizeBytes: 2_097_152,
  state: "available",
};

describe("ArtifactWorkflow", () => {
  it("renders a scoped loading state before metadata arrives", () => {
    const markup = renderToStaticMarkup(
      <ArtifactWorkflow client={{} as DataGroundClient} reference={reference} />,
    );

    assert.match(markup, /Loading metadata/u);
    assert.match(markup, new RegExp(reference.artifactId, "u"));
  });

  it("retains confirmed metadata when a refresh fails", () => {
    const current = { client, artifact, loading: true, referenceKey };
    const state = artifactWorkflowReducer(current, {
      client,
      referenceKey,
      result: {
        error: {
          code: "WORKBENCH_NETWORK_UNAVAILABLE",
          message: "The Workbench could not reach DataGround.",
          retryable: true,
        },
        ok: false,
      },
      type: "load-finished",
    });

    assert.equal(state.artifact, artifact);
    assert.equal(state.loading, false);
    assert.equal(state.error?.code, "WORKBENCH_NETWORK_UNAVAILABLE");
  });

  it("keeps confirmed metadata visible while clearing an old error for refresh", () => {
    const state = artifactWorkflowReducer(
      {
        client,
        artifact,
        error: {
          code: "WORKBENCH_NETWORK_UNAVAILABLE",
          message: "The Workbench could not reach DataGround.",
          retryable: true,
        },
        loading: false,
        referenceKey,
      },
      { client, referenceKey, type: "load-started" },
    );

    assert.equal(state.artifact, artifact);
    assert.equal(state.error, undefined);
    assert.equal(state.loading, true);
  });

  it("replaces stale confirmed metadata after a successful refresh", () => {
    const refreshed: InvocationArtifact = { ...artifact, state: "deleted" };
    const state = artifactWorkflowReducer(
      { client, artifact, loading: true, referenceKey },
      {
        client,
        referenceKey,
        result: { artifact: refreshed, ok: true },
        type: "load-finished",
      },
    );

    assert.equal(state.artifact, refreshed);
    assert.equal(state.error, undefined);
    assert.equal(state.loading, false);
  });

  it("clears prior scope immediately when another artifact starts loading", () => {
    const state = artifactWorkflowReducer(
      { client, artifact, loading: false, referenceKey },
      {
        client,
        referenceKey: `${reference.isolationDomainId}:${reference.invocationId}:art_00000000000000000002`,
        type: "load-started",
      },
    );

    assert.equal(state.artifact, undefined);
    assert.equal(state.loading, true);
  });

  it("ignores late metadata from a prior artifact scope", () => {
    const state = {
      client,
      loading: true,
      referenceKey: `${reference.isolationDomainId}:${reference.invocationId}:art_00000000000000000002`,
    };
    const completed = artifactWorkflowReducer(state, {
      client,
      referenceKey,
      result: { artifact, ok: true },
      type: "load-finished",
    });

    assert.equal(completed, state);
  });
});

it("clears metadata when the identity changes on the same artifact reference", () => {
  const otherClient = {} as DataGroundClient;
  const state = artifactWorkflowReducer(
    { client, artifact, loading: false, referenceKey },
    { client: otherClient, referenceKey, type: "load-started" },
  );
  assert.equal(state.artifact, undefined);
  assert.equal(state.client, otherClient);
  assert.equal(
    artifactWorkflowReducer(state, {
      client,
      referenceKey,
      type: "load-finished",
      result: { ok: true, artifact },
    }),
    state,
  );
});
