import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import type { PublishedServiceRevisionResource } from "../revisions/publicationClient";
import type { ServiceAliasResource } from "./aliasClient";
import {
  createAliasIdempotencyKey,
  ServiceAliasAssignWorkflow,
  validateServiceAliasName,
} from "./ServiceAliasAssignWorkflow";

const revision: PublishedServiceRevisionResource = {
  metadata: {
    createdAt: "2026-08-14T16:00:00Z",
    createdBy: "reference-runtime",
    generation: 1,
    id: "rev_00000000000000000001",
    isolationDomainId: "iso_00000000000000000001",
    updatedAt: "2026-08-14T16:01:00Z",
    version: 2,
  },
  publishedAt: "2026-08-14T16:01:00Z",
  requiredCapabilities: ["tool", "usage"],
  revisionNumber: 2,
  runtimeProfile: "reference/v1",
  serviceId: "svc_00000000000000000001",
  state: "published",
};

const current: ServiceAliasResource = {
  metadata: {
    createdAt: "2026-08-14T15:00:00Z",
    createdBy: "reference-runtime",
    generation: 4,
    id: "als_00000000000000000001",
    isolationDomainId: revision.metadata.isolationDomainId,
    updatedAt: "2026-08-14T15:30:00Z",
    version: 4,
  },
  name: "stable",
  revisionId: "rev_00000000000000000002",
  serviceId: revision.serviceId,
};

describe("ServiceAliasAssignWorkflow", () => {
  it("creates stable alias command identifiers", () => {
    assert.equal(
      createAliasIdempotencyKey(() => "00000000-0000-4000-8000-000000000001"),
      "alias:00000000000040008000000000000001",
    );
  });

  it("validates the closed alias-name contract", () => {
    assert.equal(validateServiceAliasName("stable"), undefined);
    assert.equal(validateServiceAliasName("candidate-2"), undefined);
    assert.match(validateServiceAliasName("") ?? "", /required/u);
    assert.match(validateServiceAliasName("Stable") ?? "", /lowercase/u);
    assert.match(validateServiceAliasName("candidate-") ?? "", /lowercase/u);
    assert.match(validateServiceAliasName(`a${"b".repeat(63)}`) ?? "", /63 bytes/u);
  });

  it("renders a new alias without sending a request during render", () => {
    let requested = false;
    const markup = renderToStaticMarkup(
      <ServiceAliasAssignWorkflow
        canAssign
        client={
          {
            PUT: async () => {
              requested = true;
              throw new Error("must not run during render");
            },
          } as unknown as DataGroundClient
        }
        revision={revision}
      />,
    );

    assert.equal(requested, false);
    assert.match(markup, /Ready to assign/u);
    assert.match(markup, /Expected alias version/u);
    assert.match(markup, />0<\/dd>/u);
    assert.match(markup, /value="stable"/u);
  });

  it("renders an observed move with exact optimistic state", () => {
    const markup = renderToStaticMarkup(
      <ServiceAliasAssignWorkflow
        canAssign
        client={{} as DataGroundClient}
        currentAlias={current}
        revision={revision}
      />,
    );

    assert.match(markup, /Ready to move/u);
    assert.match(markup, /als_00000000000000000001/u);
    assert.match(markup, /rev_00000000000000000002/u);
    assert.match(markup, />4<\/dd>/u);
    assert.doesNotMatch(markup, /name="service-alias"/u);
  });

  it("renders observer scope without enabling routing", () => {
    const markup = renderToStaticMarkup(
      <ServiceAliasAssignWorkflow
        canAssign={false}
        client={{} as DataGroundClient}
        disabledReason="Only service routers may assign aliases."
        revision={revision}
      />,
    );

    assert.match(markup, /Observer access only/u);
    assert.match(markup, /iso_00000000000000000001/u);
    assert.match(markup, /rev_00000000000000000001/u);
    assert.match(markup, /disabled/u);
  });

  it("recognizes an already-routed alias without sending a request", () => {
    const markup = renderToStaticMarkup(
      <ServiceAliasAssignWorkflow
        canAssign
        client={{} as DataGroundClient}
        currentAlias={{ ...current, revisionId: revision.metadata.id }}
        revision={revision}
      />,
    );

    assert.match(markup, /Already routed/u);
    assert.match(markup, /No routing command is needed/u);
    assert.doesNotMatch(markup, /Review routing/u);
  });
});
