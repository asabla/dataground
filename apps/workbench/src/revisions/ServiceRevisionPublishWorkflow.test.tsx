import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import type { ServiceRevisionResource } from "./client";
import {
  createPublicationIdempotencyKey,
  ServiceRevisionPublishWorkflow,
} from "./ServiceRevisionPublishWorkflow";

const revision: ServiceRevisionResource = {
  inputSchema: { properties: { prompt: { type: "string" } }, type: "object" },
  metadata: {
    createdAt: "2026-08-14T16:00:00Z",
    createdBy: "reference-runtime",
    generation: 1,
    id: "rev_00000000000000000001",
    isolationDomainId: "iso_00000000000000000001",
    updatedAt: "2026-08-14T16:00:00Z",
    version: 1,
  },
  requiredCapabilities: ["tool", "usage"],
  revisionNumber: 2,
  runtimeProfile: "reference/v1",
  serviceId: "svc_00000000000000000001",
  state: "draft",
};

describe("ServiceRevisionPublishWorkflow", () => {
  it("creates stable publication command identifiers", () => {
    assert.equal(
      createPublicationIdempotencyKey(() => "00000000-0000-4000-8000-000000000001"),
      "publication:00000000000040008000000000000001",
    );
  });

  it("renders an exact draft snapshot without publishing during render", () => {
    let requested = false;
    const markup = renderToStaticMarkup(
      <ServiceRevisionPublishWorkflow
        canPublish
        client={
          {
            POST: async () => {
              requested = true;
              throw new Error("must not run during render");
            },
          } as unknown as DataGroundClient
        }
        revision={revision}
      />,
    );

    assert.equal(requested, false);
    assert.match(markup, /Draft revision/u);
    assert.match(markup, /Expected version/u);
    assert.match(markup, />1<\/dd>/u);
    assert.match(markup, /Input schema/u);
    assert.match(markup, /Declared/u);
  });

  it("renders observer scope without enabling publication", () => {
    const markup = renderToStaticMarkup(
      <ServiceRevisionPublishWorkflow
        canPublish={false}
        client={{} as DataGroundClient}
        disabledReason="Only service publishers may publish revisions."
        revision={revision}
      />,
    );

    assert.match(markup, /Observer access only/u);
    assert.match(markup, /iso_00000000000000000001/u);
    assert.match(markup, /rev_00000000000000000001/u);
    assert.match(markup, /disabled/u);
  });
});
