import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import {
  AgentServiceAuthoringWorkflow,
  isServiceSelectedForScope,
} from "./AgentServiceAuthoringWorkflow";
import type { AgentServiceResource } from "./client";

const isolationDomainId = "iso_00000000000000000001";
const service: AgentServiceResource = {
  metadata: {
    createdAt: "2026-08-15T00:00:00Z",
    createdBy: "usr_00000000000000000001",
    generation: 1,
    id: "svc_00000000000000000001",
    isolationDomainId,
    updatedAt: "2026-08-15T00:00:00Z",
    version: 1,
  },
  name: "Research agent",
};

function renderWorkflow(
  selectedService?: AgentServiceResource,
  activeIsolationDomainId = isolationDomainId,
): string {
  return renderToStaticMarkup(
    <AgentServiceAuthoringWorkflow
      canCreateRevision
      canCreateService
      client={{} as DataGroundClient}
      isolationDomainId={activeIsolationDomainId}
      onOpenService={() => undefined}
      selectedService={selectedService}
    />,
  );
}

describe("AgentServiceAuthoringWorkflow", () => {
  it("keeps revision drafting unavailable until a service is explicitly opened", () => {
    const markup = renderWorkflow();

    assert.match(markup, /Create agent service/u);
    assert.doesNotMatch(markup, /Create revision draft/u);
  });

  it("opens revision drafting for the exact selected service and isolation scope", () => {
    const markup = renderWorkflow(service);

    assert.equal(isServiceSelectedForScope(service, isolationDomainId), true);
    assert.match(markup, /Create agent service/u);
    assert.match(markup, /Create revision draft/u);
    assert.match(markup, /svc_00000000000000000001/u);
    assert.match(markup, /reference\/v1/u);
  });

  it("fails closed when selected service state crosses the active isolation scope", () => {
    const crossScopeService: AgentServiceResource = {
      ...service,
      metadata: {
        ...service.metadata,
        isolationDomainId: "iso_00000000000000000002",
      },
    };
    const markup = renderWorkflow(crossScopeService);

    assert.equal(isServiceSelectedForScope(crossScopeService, isolationDomainId), false);
    assert.match(markup, /Scope mismatch/u);
    assert.match(markup, /Revision drafting unavailable/u);
    assert.doesNotMatch(markup, /Create revision draft/u);
    assert.doesNotMatch(markup, /iso_00000000000000000002/u);
    assert.doesNotMatch(markup, /svc_00000000000000000001/u);
  });

  it("rejects matching but malformed scope before exposing revision drafting", () => {
    const malformedService: AgentServiceResource = {
      ...service,
      metadata: { ...service.metadata, isolationDomainId: "invalid" },
    };
    const markup = renderWorkflow(malformedService, "invalid");

    assert.equal(isServiceSelectedForScope(malformedService, "invalid"), false);
    assert.match(markup, /Scope mismatch/u);
    assert.doesNotMatch(markup, /Create revision draft/u);
  });
});
