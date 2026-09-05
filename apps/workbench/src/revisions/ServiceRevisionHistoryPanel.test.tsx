import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import type { ServiceRevisionHistoryResource } from "./client";
import { ServiceRevisionHistoryPanel } from "./ServiceRevisionHistoryPanel";

const revision: ServiceRevisionHistoryResource = {
  metadata: {
    createdAt: "2026-08-31T08:00:00Z",
    createdBy: "usr_00000000000000000001",
    generation: 2,
    id: "rev_00000000000000000001",
    isolationDomainId: "iso_00000000000000000001",
    updatedAt: "2026-08-31T08:05:00Z",
    version: 2,
  },
  publishedAt: "2026-08-31T08:05:00Z",
  requiredCapabilities: [],
  revisionNumber: 2,
  runtimeProfile: "reference/v1",
  serviceId: "svc_00000000000000000001",
  state: "published",
};

function render(
  properties: Partial<React.ComponentProps<typeof ServiceRevisionHistoryPanel>> = {},
) {
  return renderToStaticMarkup(
    <ServiceRevisionHistoryPanel
      isLoading={false}
      isLoadingMore={false}
      onLoadMore={() => undefined}
      onRetry={() => undefined}
      revisions={[]}
      {...properties}
    />,
  );
}

describe("service revision history panel", () => {
  it("distinguishes loading, unavailable, and empty history", () => {
    assert.match(render({ isLoading: true }), /Loading revision history/u);
    assert.match(render(), /No revisions yet/u);
    const unavailable = render({
      error: { code: "UNAVAILABLE", message: "Revision history is unavailable.", retryable: true },
    });
    assert.match(unavailable, /role="alert"/u);
    assert.match(unavailable, /Retry revision discovery/u);
    assert.doesNotMatch(unavailable, /No revisions yet/u);
  });

  it("offers retirement only for published revisions when a handler is available", () => {
    assert.doesNotMatch(render({ revisions: [revision] }), /Retire revision/u);
    const markup = render({
      revisions: [
        revision,
        {
          ...revision,
          metadata: { ...revision.metadata, id: "rev_00000000000000000002" },
          revisionNumber: 3,
          state: "retired",
        },
      ],
      onRetire: () => undefined,
    });
    assert.match(markup, /Retire revision 2/u);
    assert.doesNotMatch(markup, /Retire revision 3/u);
  });

  it("renders state text, runtime profile, time, and pagination", () => {
    const markup = render({ nextCursor: "next", revisions: [revision] });
    assert.match(markup, /Revision 2/u);
    assert.match(markup, />published</u);
    assert.match(markup, /reference\/v1/u);
    assert.match(markup, /dateTime="2026-08-31T08:05:00Z"/u);
    assert.match(markup, /Load more revisions/u);
  });
});

it("offers explicit creation and reopening only with callbacks and excludes retired revisions", () => {
  const markup = render({
    revisions: [revision, { ...revision, revisionNumber: 3, state: "retired" }],
    onCreate: () => {},
    onOpen: () => {},
  });
  assert.match(markup, /New revision/u);
  assert.match(markup, /Open revision 2/u);
  assert.doesNotMatch(markup, /Open revision 3/u);
  assert.doesNotMatch(render({ revisions: [revision] }), /Open revision|New revision/u);
  assert.match(render({ isLoading: true, onCreate: () => {} }), /disabled/u);
});
