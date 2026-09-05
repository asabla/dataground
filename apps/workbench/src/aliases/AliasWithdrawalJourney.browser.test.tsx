import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it } from "vitest";
import { DevelopmentWorkbench } from "../App";
import type { DataGroundClient } from "../contracts/client";

it("opens a service, withdraws its last alias, rediscovers routing, and retires the revision", async () => {
  (
    globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
  const scope = "iso_00000000000000000001";
  const metadata = {
    isolationDomainId: scope,
    createdBy: "operator",
    createdAt: "2026-09-05T12:00:00Z",
    updatedAt: "2026-09-05T12:01:00Z",
    version: 1,
    generation: 1,
  };
  const service = {
    name: "Withdrawal journey",
    metadata: { ...metadata, id: "svc_00000000000000000001" },
  };
  const revision = {
    serviceId: service.metadata.id,
    revisionNumber: 1,
    state: "published",
    runtimeProfile: "reference/v1",
    requiredCapabilities: [],
    publishedAt: "2026-09-05T12:01:00Z",
    metadata: { ...metadata, id: "rev_00000000000000000001", version: 2, generation: 2 },
    inputSchema: {
      type: "object",
      additionalProperties: false,
      required: ["prompt"],
      properties: { prompt: { type: "string", minLength: 1, maxLength: 1024 } },
    },
  };
  const alias = {
    name: "stable",
    serviceId: service.metadata.id,
    revisionId: revision.metadata.id,
    metadata: { ...metadata, id: "als_00000000000000000001" },
  };
  let withdrawn = false;
  let retired = false;
  let aliasReads = 0;
  const commands: string[] = [];
  const client = {
    GET: async (path: string) => {
      if (path.endsWith("/agent-services"))
        return { data: { items: [service] }, response: new Response(null, { status: 200 }) };
      if (path.endsWith("/revisions"))
        return { data: { items: [revision] }, response: new Response(null, { status: 200 }) };
      if (path.endsWith("/aliases/{alias}")) {
        aliasReads++;
        return withdrawn
          ? {
              error: {
                error: {
                  code: "SERVICE_ALIAS_NOT_FOUND",
                  message: "Service alias was not found.",
                  correlationId: "cor_route",
                  retryable: false,
                },
              },
              response: new Response(null, { status: 404 }),
            }
          : { data: alias, response: new Response(null, { status: 200 }) };
      }
      throw new Error(`Unexpected read: ${path}`);
    },
    POST: async (path: string, options: { body: { expectedVersion: number } }) => {
      commands.push(path);
      if (path.endsWith("/actions/withdraw")) {
        expect(options.body.expectedVersion).toBe(1);
        withdrawn = true;
        const now = "2026-09-05T12:02:00Z";
        return {
          data: {
            ...alias,
            withdrawnAt: now,
            metadata: { ...alias.metadata, generation: 2, version: 2, updatedAt: now },
          },
          response: new Response(null, { status: 200 }),
        };
      }
      if (path.endsWith("/actions/retire")) {
        expect(withdrawn).toBe(true);
        expect(options.body.expectedVersion).toBe(2);
        retired = true;
        return {
          data: {
            ...revision,
            state: "retired",
            metadata: {
              ...revision.metadata,
              generation: 3,
              version: 3,
              updatedAt: "2026-09-05T12:03:00Z",
            },
          },
          response: new Response(null, { status: 200 }),
        };
      }
      throw new Error(`Unexpected command: ${path}`);
    },
  } as unknown as DataGroundClient;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  async function click(text: string, contains = false) {
    const button = Array.from(host.querySelectorAll("button")).find((element) =>
      contains ? element.textContent?.includes(text) : element.textContent === text,
    );
    expect(button, host.textContent ?? "").toBeDefined();
    await act(async () => button?.click());
  }
  try {
    await act(async () =>
      root.render(
        <DevelopmentWorkbench client={client} isolationDomainId={scope} onDisconnect={() => {}} />,
      ),
    );
    await click("Withdrawal journey", true);
    expect(aliasReads).toBe(1);
    expect(commands).toHaveLength(0);
    await click("Withdraw stable alias");
    expect(commands).toHaveLength(0);
    await click("Confirm alias withdrawal");
    expect(withdrawn).toBe(true);
    expect(host.textContent).toContain("Alias withdrawn");
    await click("Back to service");
    expect(aliasReads).toBe(2);
    expect(host.textContent).not.toContain("Withdraw stable alias");
    await click("Retire revision 1");
    await click("Confirm retirement");
    expect(retired).toBe(true);
    expect(host.textContent).toContain("Revision retired");
    expect(commands).toHaveLength(2);
  } finally {
    await act(async () => root.unmount());
    host.remove();
  }
});
