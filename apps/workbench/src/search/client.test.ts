import { expect, it, vi } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import {
  compareSearchCreatedAt,
  type SearchResult,
  searchResources,
  validSearchText,
} from "./client";

const scope = "iso_00000000000000000001";
const result = (kind: SearchResult["kind"], prefix: string): SearchResult =>
  ({
    metadata: {
      id: `${prefix}_00000000000000000001`,
      isolationDomainId: scope,
      createdAt: "2026-10-29T08:00:00Z",
    },
    kind,
    name: "Research",
    ...(kind === "invocation" || kind === "artifact"
      ? { serviceId: "svc_00000000000000000001", state: "failed" as const }
      : {}),
    ...(kind === "artifact" ? { invocationId: "inv_00000000000000000001" } : {}),
  }) as SearchResult;
const workspace = result("workspace", "wsp");
const response = (data: unknown, status = 200) => ({
  data,
  response: new Response(null, { status }),
});
const client = (data: unknown) =>
  ({ GET: vi.fn(async () => response(data)) }) as unknown as DataGroundClient;

it("searches the authorized collection with bounded text and paging parameters", async () => {
  const items = [
    workspace,
    result("agent-service", "svc"),
    result("invocation", "inv"),
    result("artifact", "art"),
  ];
  const api = client({ items, nextCursor: "next_page" });
  expect(await searchResources(api, scope, "RESEARCH")).toEqual({
    ok: true,
    page: { items, nextCursor: "next_page" },
  });
  expect(api.GET).toHaveBeenCalledWith("/v1/isolation-domains/{isolationDomainId}/search", {
    params: { path: { isolationDomainId: scope }, query: { q: "RESEARCH", limit: 50 } },
  });
  const filtered = client({ items: [items[3]] });
  expect((await searchResources(filtered, scope, "research", "artifact", "previous_page")).ok).toBe(
    true,
  );
  expect(filtered.GET).toHaveBeenCalledWith(expect.any(String), {
    params: {
      path: { isolationDomainId: scope },
      query: { q: "research", kind: "artifact", limit: 50, cursor: "previous_page" },
    },
  });
});
it("rejects cross-scope, malformed, duplicate, unordered and filter-mismatched results", async () => {
  const foreign = {
    ...workspace,
    metadata: { ...workspace.metadata, isolationDomainId: "iso_00000000000000000002" },
  };
  for (const items of [
    [foreign],
    [{ ...workspace, kind: "future" }],
    [{ ...workspace, metadata: { ...workspace.metadata, id: "art_00000000000000000001" } }],
    [workspace, workspace],
    [result("artifact", "art"), workspace],
    [{ ...workspace, name: "Unrelated" }],
    [{ ...workspace, metadata: { ...workspace.metadata, createdAt: "bad" } }],
    [{ ...result("artifact", "art"), invocationId: undefined }],
    [{ ...result("invocation", "inv"), state: "future" }],
    [{ ...workspace, name: "x".repeat(513) }],
    Array.from({ length: 51 }, () => workspace),
  ]) {
    expect((await searchResources(client({ items }), scope, "research")).ok).toBe(false);
  }
  expect(
    (await searchResources(client({ items: [workspace] }), scope, "research", "artifact")).ok,
  ).toBe(false);
});
it("rejects nonadvancing and empty-page cursors", async () => {
  for (const data of [
    { items: [], nextCursor: "next" },
    { items: [workspace], nextCursor: "same" },
    { items: [workspace], nextCursor: "!" },
    { items: [workspace], nextCursor: "a".repeat(513) },
  ])
    expect((await searchResources(client(data), scope, "research", undefined, "same")).ok).toBe(
      false,
    );
});
it("keeps only safe navigation metadata and uses literal ASCII folding", async () => {
  const item = {
    ...workspace,
    name: "Budget_%",
    description: "Research É",
    secret: "private response",
    metadata: { ...workspace.metadata, createdBy: "private actor" },
  };
  const api = client({ items: [item] });
  expect(await searchResources(api, scope, "budget_%")).toEqual({
    ok: true,
    page: { items: [{ ...workspace, name: item.name, description: item.description }] },
  });
  expect((await searchResources(api, scope, "é")).ok).toBe(false);
  expect((await searchResources(api, scope, "É")).ok).toBe(true);
});
it("validates input without contacting the API", async () => {
  const api = client({ items: [] });
  for (const query of ["", "x".repeat(129), "a\nb", "\ud800"])
    expect((await searchResources(api, scope, query)).ok).toBe(false);
  expect((await searchResources(api, "bad", "x")).ok).toBe(false);
  expect((await searchResources(api, scope, "x", undefined, "!")).ok).toBe(false);
  expect(api.GET).not.toHaveBeenCalled();
  expect(validSearchText("é".repeat(128))).toBe(true);
  expect(validSearchText("\u{10400}".repeat(128))).toBe(true);
});
it("retains sub-millisecond page order and equivalent time zones", async () => {
  const newer = {
    ...result("artifact", "art"),
    metadata: { ...result("artifact", "art").metadata, createdAt: "2026-10-29T08:00:00.000002Z" },
  };
  const older = {
    ...workspace,
    metadata: { ...workspace.metadata, createdAt: "2026-10-29T08:00:00.000001Z" },
  };
  expect((await searchResources(client({ items: [newer, older] }), scope, "research")).ok).toBe(
    true,
  );
  expect((await searchResources(client({ items: [older, newer] }), scope, "research")).ok).toBe(
    false,
  );
  expect(
    compareSearchCreatedAt("2026-10-29T08:00:00.001000001Z", "2026-10-29T09:00:00.001000001+01:00"),
  ).toBe(0);
});
it("keeps safe denials and hides malformed or thrown upstream responses", async () => {
  const error = {
    code: "ACCESS_DENIED",
    message: "Search access is denied.",
    retryable: false,
    correlationId: "cor-review",
  };
  const denied = {
    GET: async () => ({ error: { error }, response: new Response(null, { status: 403 }) }),
  } as unknown as DataGroundClient;
  expect(await searchResources(denied, scope, "x")).toEqual({
    ok: false,
    error: { ...error, status: 403 },
  });
  const broken = {
    GET: async () => {
      throw new Error("private upstream response");
    },
  } as unknown as DataGroundClient;
  expect(JSON.stringify(await searchResources(broken, scope, "x"))).not.toContain(
    "private upstream",
  );
  expect((await searchResources(client(undefined), scope, "x")).ok).toBe(false);
});
