import assert from "node:assert/strict";
import { describe, it } from "vitest";
import type { ServiceRevisionHistoryResource } from "./client";
import { resumeServiceRevision } from "./discovery";

const published: ServiceRevisionHistoryResource = {
  inputSchema: { properties: { prompt: { type: "string" } }, type: "object" },
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
  requiredCapabilities: ["tool", "usage"],
  revisionNumber: 1,
  runtimeProfile: "reference/v1",
  serviceId: "svc_00000000000000000001",
  state: "published",
};

describe("service revision discovery selection", () => {
  it("resumes a published immutable definition at alias routing", () => {
    const selection = resumeServiceRevision(published);

    assert.equal(selection.revision?.state, "draft");
    assert.equal(selection.revision?.metadata.version, 1);
    assert.equal(selection.revision?.metadata.updatedAt, published.metadata.createdAt);
    assert.equal(selection.publishedRevision?.state, "published");
    assert.equal(selection.publishedRevision?.metadata.version, 2);
    assert.equal(selection.publishedRevision?.publishedAt, published.publishedAt);
    assert.notEqual(selection.revision?.inputSchema, published.inputSchema);
  });

  it("resumes a draft directly and leaves retired history read-only", () => {
    const { publishedAt: _, ...publishedDefinition } = published;
    const draft = { ...publishedDefinition, state: "draft" as const };
    const draftSelection = resumeServiceRevision(draft);
    assert.equal(draftSelection.revision?.metadata.version, 2);
    assert.equal(draftSelection.publishedRevision, undefined);

    const retired = { ...published, state: "retired" as const };
    assert.deepEqual(resumeServiceRevision(retired), {});
  });
});
