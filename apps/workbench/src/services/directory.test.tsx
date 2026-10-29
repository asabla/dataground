import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import type { AgentServiceResource } from "./client";
import { selectDirectoryServices } from "./directory";
import { ServiceDirectory } from "./ServiceDirectory";

function service(id: string, name: string, updatedAt: string): AgentServiceResource {
  return {
    name,
    description: "Review source changes",
    metadata: {
      id,
      isolationDomainId: "iso_00000000000000000001",
      createdAt: "2026-01-01T00:00:00Z",
      updatedAt,
      createdBy: "usr_00000000000000000001",
      generation: 1,
      version: 1,
    },
  };
}
const older = service("svc_00000000000000000001", "Alpha", "2026-01-02T00:00:00Z");
const newer = service("svc_00000000000000000002", "Beta", "2026-01-03T00:00:00Z");
const noop = () => undefined;
const props = {
  services: [older, newer],
  loading: false,
  loadingMore: false,
  hasMore: true,
  query: "",
  sort: "updated" as const,
  onQueryChange: noop,
  onSortChange: noop,
  onRefresh: noop,
  onLoadMore: noop,
  onCreate: noop,
  onOpen: noop,
  onHistory: noop,
};

describe("service directory", () => {
  it("searches names, descriptions and exact IDs without changing the source page", () => {
    const resources = [older, newer];
    assert.deepEqual(selectDirectoryServices(resources, "  ALPHA ", "updated"), [older]);
    assert.deepEqual(selectDirectoryServices(resources, newer.metadata.id, "updated"), [newer]);
    assert.deepEqual(selectDirectoryServices(resources, "source", "updated"), [newer, older]);
    assert.deepEqual(selectDirectoryServices(resources, "missing", "name"), []);
    assert.deepEqual(resources, [older, newer]);
  });

  it("sorts by metadata update or name with stable ID ordering on ties", () => {
    assert.deepEqual(selectDirectoryServices([newer, older], "", "name"), [older, newer]);
    const tied = {
      ...newer,
      name: older.name,
      metadata: { ...newer.metadata, updatedAt: older.metadata.updatedAt },
    };
    assert.deepEqual(selectDirectoryServices([tied, older], "", "updated"), [older, tied]);
    assert.deepEqual(selectDirectoryServices([tied, older], "", "name"), [older, tied]);
  });

  it("presents comparable resources in a named table without foregrounding internal IDs", () => {
    const markup = renderToStaticMarkup(<ServiceDirectory {...props} />);
    assert.match(markup, /<table[^>]+aria-label="Loaded agent services"/u);
    assert.match(markup, /role="rowheader"/u);
    assert.match(markup, /<time datetime=/iu);
    assert.doesNotMatch(markup, /<code>svc_/u);
  });

  it("keeps page loading available when the loaded-page search has no matches", () => {
    const markup = renderToStaticMarkup(<ServiceDirectory {...props} query="missing" />);
    assert.match(markup, /No matching loaded services/u);
    assert.match(markup, /0 of 2 loaded services shown/u);
    assert.match(markup, /More services are available/u);
    assert.match(markup, /Load more services/u);
    assert.doesNotMatch(markup, /Create your first agent service/u);
  });

  it("disables stale service actions on refresh or failure and does not invent runtime status", () => {
    for (const state of [
      { loading: true },
      { error: { code: "UNAVAILABLE", message: "Try again.", retryable: true } },
    ]) {
      const markup = renderToStaticMarkup(<ServiceDirectory {...props} {...state} />);
      const actions = markup.match(
        /<button[^>]*aria-label="(?:Open service|View interactions)[^>]*>/gu,
      );
      assert.equal(actions?.length, 4);
      for (const action of actions ?? []) assert.match(action, /disabled/u);
      assert.doesNotMatch(markup, /Active interaction|Needs review|Ready/u);
    }
  });

  it("distinguishes an empty scope from a failed read and escapes untrusted names", () => {
    assert.match(
      renderToStaticMarkup(<ServiceDirectory {...props} services={[]} hasMore={false} />),
      /Create your first agent service/u,
    );
    const failed = renderToStaticMarkup(
      <ServiceDirectory
        {...props}
        services={[]}
        error={{ code: "DENIED", message: "Access denied.", retryable: false }}
      />,
    );
    assert.doesNotMatch(failed, /Create your first agent service/u);
    assert.match(failed, /Access denied/u);
    const escaped = renderToStaticMarkup(
      <ServiceDirectory {...props} services={[{ ...older, name: "<script>alert(1)</script>" }]} />,
    );
    assert.doesNotMatch(escaped, /<script>/u);
    assert.match(escaped, /&lt;script&gt;/u);
  });
});
