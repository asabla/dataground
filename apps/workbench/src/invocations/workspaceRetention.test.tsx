import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import { createDataGroundClient } from "../contracts/client";
import { WorkspaceRetentionStatus } from "./WorkspaceRetentionWorkflow";
import { readWorkspaceRetention, type WorkspaceRetention } from "./workspaceRetention";

const reference = {
  isolationDomainId: "iso_00000000000000000001",
  invocationId: "inv_00000000000000000001",
};
const value: WorkspaceRetention = {
  schemaVersion: "dataground.invocation-workspace-retention/v1",
  ...reference,
  state: "export_pending",
  recovery: "review_required",
};
function client(body: unknown, status = 200) {
  return createDataGroundClient("http://localhost", {
    fetch: async (request) => {
      assert.match(
        (request as Request).url,
        /\/invocations\/inv_00000000000000000001\/workspace-retention$/u,
      );
      assert.equal((request as Request).cache, "no-store");
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    },
  });
}
describe("workspace retention", () => {
  it("reads scoped metadata without requesting artifact contents", async () => {
    assert.deepEqual(
      await readWorkspaceRetention(client({ ...value, unexpectedContent: "private" }), reference),
      {
        kind: "available",
        value,
      },
    );
  });
  it("rejects foreign, unknown and incomplete evidence", async () => {
    for (const changed of [
      { isolationDomainId: "iso_00000000000000000002" },
      { invocationId: "inv_00000000000000000002" },
      { schemaVersion: "future" },
      { state: "unknown" },
      { recovery: "eligible" },
      { state: "recorded" },
      { artifactId: "art_00000000000000000001" },
      { state: "recorded", artifactId: "art_00000000000000000001", recordedAt: "invalid" },
    ])
      assert.equal(
        (await readWorkspaceRetention(client({ ...value, ...changed }), reference)).kind,
        "error",
      );
  });
  it("keeps missing and denied metadata distinct and does not echo server errors", async () => {
    assert.deepEqual(await readWorkspaceRetention(client({}, 404), reference), { kind: "missing" });
    for (const status of [401, 403, 500]) {
      const result = await readWorkspaceRetention(
        client({ error: { message: "private upstream detail" } }, status),
        reference,
      );
      assert.equal(result.kind, "error");
      assert.doesNotMatch(JSON.stringify(result), /private upstream/u);
    }
  });
  it("keeps the recovery blocker visible and offers no execution or deletion action", () => {
    const markup = renderToStaticMarkup(
      <WorkspaceRetentionStatus
        loading={false}
        result={{ kind: "available", value }}
        onRefresh={() => {}}
      />,
    );
    assert.match(markup, /Cancellation reached its deadline/u);
    assert.match(markup, /An operator must review/u);
    assert.match(markup, /Changes not yet retained/u);
    assert.doesNotMatch(markup, />Retry execution<|>Delete|>Recover</u);
  });
  it("presents recorded evidence without claiming content access", () => {
    const markup = renderToStaticMarkup(
      <WorkspaceRetentionStatus
        loading={false}
        result={{
          kind: "available",
          value: {
            ...value,
            state: "recorded",
            recovery: "requested",
            artifactId: "art_00000000000000000001",
            recordedAt: "2026-11-07T23:00:00Z",
          },
        }}
        onRefresh={() => {}}
      />,
    );
    assert.match(markup, /Changes recorded/u);
    assert.match(markup, /Recovery was requested/u);
    assert.match(markup, /File access and availability are checked separately/u);
    assert.match(markup, /disabled/u);
  });
});
