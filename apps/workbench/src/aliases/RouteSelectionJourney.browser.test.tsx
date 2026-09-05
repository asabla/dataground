import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it } from "vitest";
import { DevelopmentWorkbench } from "../App";
import type { DataGroundClient } from "../contracts/client";

const scope = "iso_00000000000000000001";
const metadata = {
  isolationDomainId: scope,
  createdBy: "operator",
  createdAt: "2026-09-05T12:00:00Z",
  updatedAt: "2026-09-05T12:01:00Z",
  version: 2,
  generation: 2,
};
const service = {
  name: "Exact route journey",
  metadata: { ...metadata, id: "svc_00000000000000000001" },
};
const older = {
  metadata: { ...metadata, id: "rev_00000000000000000001" },
  serviceId: service.metadata.id,
  revisionNumber: 1,
  state: "published",
  publishedAt: metadata.updatedAt,
  runtimeProfile: "reference/v1",
  requiredCapabilities: [],
  inputSchema: {
    type: "object",
    additionalProperties: false,
    required: ["prompt"],
    properties: { prompt: { type: "string", title: "Older prompt", minLength: 1, maxLength: 512 } },
  },
};
const newer = {
  ...older,
  metadata: { ...metadata, id: "rev_00000000000000000080" },
  revisionNumber: 80,
  inputSchema: {
    ...older.inputSchema,
    properties: { prompt: { ...older.inputSchema.properties.prompt, title: "Newer prompt" } },
  },
};
const stable = {
  name: "stable",
  serviceId: service.metadata.id,
  revisionId: older.metadata.id,
  metadata: { ...metadata, id: "als_00000000000000000001" },
};
const canary = {
  ...stable,
  name: "canary",
  revisionId: newer.metadata.id,
  metadata: { ...metadata, id: "als_00000000000000000002" },
};
const ok = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) });
const denied = (status: number, code: string) => ({
  error: {
    error: {
      code,
      message: "Route or revision is unavailable.",
      correlationId: "cor_route",
      retryable: false,
    },
  },
  response: new Response(null, { status }),
});
type ReadOptions = { params: { path: { alias?: string; revisionId?: string } } };
let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  (
    globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});
async function click(text: string, contains = false) {
  const button = [...host.querySelectorAll("button")].find((value) =>
    contains ? value.textContent?.includes(text) : value.textContent === text,
  );
  expect(button, host.textContent ?? "").toBeDefined();
  await act(async () => button?.click());
}
async function open(client: DataGroundClient) {
  await act(async () =>
    root.render(
      <DevelopmentWorkbench client={client} isolationDomainId={scope} onDisconnect={() => {}} />,
    ),
  );
  await click(service.name, true);
}
function fixture(read: (path: string, options: ReadOptions) => unknown) {
  const calls: string[] = [];
  const client = {
    GET: async (path: string, options: ReadOptions) => {
      calls.push(path);
      if (path.endsWith("/agent-services")) return ok({ items: [service] });
      if (path.endsWith("/revisions")) return ok({ items: [newer], nextCursor: "history_cursor" });
      if (path.endsWith("/aliases")) return ok({ items: [canary, stable] });
      return read(path, options);
    },
    POST: async () => {
      throw new Error("unexpected mutation");
    },
  } as unknown as DataGroundClient;
  return { client, calls };
}

it("loads the routed older revision outside the history page and switches to a custom route", async () => {
  const reads: string[] = [];
  const { client, calls } = fixture((path, options) => {
    if (path.endsWith("/aliases/{alias}"))
      return ok(options.params.path.alias === "canary" ? canary : stable);
    if (path.endsWith("/service-revisions/{revisionId}")) {
      reads.push(options.params.path.revisionId ?? "");
      return ok(options.params.path.revisionId === older.metadata.id ? older : newer);
    }
    throw new Error("unexpected read");
  });
  await open(client);
  expect(host.textContent).toContain("Older prompt");
  expect(host.textContent).not.toContain("Newer prompt");
  expect(reads).toEqual([older.metadata.id]);
  expect(calls.filter((path) => path.endsWith("/revisions"))).toHaveLength(1);
  await click("Use canary route");
  expect(host.textContent).toContain("Newer prompt");
  expect(host.textContent).not.toContain("Older prompt");
  expect([...host.querySelectorAll("input")].some((input) => input.value === "canary")).toBe(true);
  expect(reads).toEqual([older.metadata.id, newer.metadata.id]);
});

it.each(["denied", "retired", "substituted"])(
  "keeps invocation unavailable for a %s exact revision",
  async (mode) => {
    const { client } = fixture((path) => {
      if (path.endsWith("/aliases/{alias}")) return ok(stable);
      return mode === "denied"
        ? denied(403, "ACCESS_DENIED")
        : ok(mode === "retired" ? { ...older, state: "retired" } : newer);
    });
    await open(client);
    expect(host.querySelector('[role="alert"]')).not.toBeNull();
    expect(host.textContent).not.toContain("Start invocation");
    expect(host.textContent).not.toContain("Newer prompt");
  },
);

it("ignores a delayed old revision read after selecting a different route", async () => {
  let finish: ((value: ReturnType<typeof ok>) => void) | undefined;
  const { client } = fixture((path, options) => {
    if (path.endsWith("/aliases/{alias}"))
      return ok(options.params.path.alias === "canary" ? canary : stable);
    return options.params.path.revisionId === older.metadata.id
      ? new Promise((done) => {
          finish = done;
        })
      : ok(newer);
  });
  await open(client);
  expect(host.textContent).toContain("Loading stable route");
  await click("Use canary route");
  expect(host.textContent).toContain("Newer prompt");
  await act(async () => finish?.(ok(older)));
  expect(host.textContent).toContain("Newer prompt");
  expect(host.textContent).not.toContain("Older prompt");
});

it("re-reads a selected alias and refuses a route withdrawn since discovery", async () => {
  const { client } = fixture((path, options) => {
    if (path.endsWith("/aliases/{alias}"))
      return options.params.path.alias === "canary"
        ? denied(404, "SERVICE_ALIAS_NOT_FOUND")
        : ok(stable);
    return ok(older);
  });
  await open(client);
  await click("Use canary route");
  expect(host.textContent).toContain("no longer active");
  expect(host.textContent).not.toContain("Start invocation");
});

it("starts a new draft while retaining the existing service and invalidates a delayed route read", async () => {
  let finish: ((value: ReturnType<typeof ok>) => void) | undefined;
  const { client } = fixture((path) =>
    path.endsWith("/aliases/{alias}")
      ? ok(stable)
      : new Promise((done) => {
          finish = done;
        }),
  );
  await open(client);
  expect(host.textContent).toContain("Loading stable route");
  await click("New revision");
  expect(host.textContent).toContain("Create revision draft");
  await act(async () => finish?.(ok(older)));
  expect(host.textContent).toContain("Create revision draft");
  expect(host.textContent).not.toContain("Older prompt");
  expect(host.textContent).not.toContain("Loading stable route");
});

it("reopens an exact published revision as the target of a version-checked alias move", async () => {
  const { client } = fixture((path, options) =>
    path.endsWith("/aliases/{alias}")
      ? ok(stable)
      : ok(options.params.path.revisionId === older.metadata.id ? older : newer),
  );
  const commands: unknown[] = [];
  const movable = {
    ...client,
    PUT: async (
      _path: string,
      options: {
        params: { path: { alias: string } };
        body: { revisionId: string; expectedVersion: number };
      },
    ) => {
      commands.push(options.body);
      expect(options.params.path.alias).toBe("stable");
      return ok({
        ...stable,
        revisionId: newer.metadata.id,
        metadata: {
          ...stable.metadata,
          version: 3,
          generation: 3,
          updatedAt: "2026-09-05T12:02:00Z",
        },
      });
    },
  } as unknown as DataGroundClient;
  await open(movable);
  await click("Open revision 80");
  expect(host.textContent).toContain("Move service alias");
  expect(host.textContent).not.toContain("Start invocation");
  expect(commands).toHaveLength(0);
  await click("Review routing");
  expect(commands).toHaveLength(0);
  await click("Confirm route change");
  expect(commands).toEqual([{ expectedVersion: 2, revisionId: newer.metadata.id }]);
});

it("rechecks a draft or retired revision when opening history instead of trusting the old row", async () => {
  let retired = false;
  const { client } = fixture((path, options) => {
    if (path.endsWith("/aliases/{alias}")) return ok(stable);
    if (options.params.path.revisionId === older.metadata.id) return ok(older);
    return ok(
      retired
        ? { ...newer, state: "retired" }
        : {
            ...newer,
            state: "draft",
            publishedAt: undefined,
            metadata: { ...newer.metadata, version: 1, generation: 1 },
          },
    );
  });
  await open(client);
  await click("Open revision 80");
  expect(host.textContent).toContain("Review publication");
  expect(host.textContent).not.toContain("Move service alias");
  retired = true;
  await click("Open revision 80");
  expect(host.textContent).toContain("This revision has retired");
  expect(host.textContent).not.toContain("Review publication");
});

it("creates and publishes another revision for the existing service without moving its route implicitly", async () => {
  const draft = {
    ...newer,
    metadata: { ...newer.metadata, id: "rev_00000000000000000081", version: 1, generation: 1 },
    revisionNumber: 81,
    state: "draft",
    publishedAt: undefined,
    inputSchema: undefined,
  };
  const publication = {
    ...draft,
    state: "published",
    publishedAt: "2026-09-05T12:02:00Z",
    metadata: { ...draft.metadata, version: 2, generation: 1, updatedAt: "2026-09-05T12:02:00Z" },
  };
  const commands: string[] = [];
  const { client } = fixture((path, options) =>
    path.endsWith("/aliases/{alias}")
      ? ok(stable)
      : ok(options.params.path.revisionId === draft.metadata.id ? publication : older),
  );
  const authoring = {
    ...client,
    POST: async (
      path: string,
      options: {
        params: { path: { serviceId?: string; revisionId?: string } };
        body: { expectedVersion?: number };
      },
    ) => {
      commands.push(path);
      if (path.endsWith("/revisions")) {
        expect(options.params.path.serviceId).toBe(service.metadata.id);
        return { data: draft, response: new Response(null, { status: 201 }) };
      }
      if (path.endsWith("/actions/publish")) {
        expect(options.params.path.revisionId).toBe(draft.metadata.id);
        expect(options.body.expectedVersion).toBe(1);
        return ok(publication);
      }
      throw new Error("unexpected mutation");
    },
    PUT: async () => {
      throw new Error("route moved without confirmation");
    },
  } as unknown as DataGroundClient;
  await open(authoring);
  await click("New revision");
  expect(commands).toHaveLength(0);
  await click("Create revision draft");
  await click("Open revision");
  await click("Review publication");
  expect(commands).toHaveLength(1);
  await click("Confirm publication");
  await click("Assign alias");
  expect(commands).toHaveLength(2);
  expect(host.textContent).toContain("Move service alias");
  expect(host.textContent).not.toContain("Start invocation");
});
