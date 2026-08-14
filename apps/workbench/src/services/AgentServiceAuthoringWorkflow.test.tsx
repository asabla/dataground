import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import type { PublishedServiceRevisionResource, ServiceRevisionResource } from "../revisions";
import {
  AgentServiceAuthoringWorkflow,
  isPublishedRevisionSelectedForService,
  isRevisionSelectedForService,
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
const revision: ServiceRevisionResource = {
  inputSchema: { properties: { prompt: { type: "string" } }, type: "object" },
  metadata: {
    createdAt: "2026-08-15T00:01:00Z",
    createdBy: "usr_00000000000000000001",
    generation: 1,
    id: "rev_00000000000000000001",
    isolationDomainId,
    updatedAt: "2026-08-15T00:01:00Z",
    version: 1,
  },
  requiredCapabilities: ["tool"],
  revisionNumber: 1,
  runtimeProfile: "reference/v1",
  serviceId: service.metadata.id,
  state: "draft",
};
const publishedRevision: PublishedServiceRevisionResource = {
  ...revision,
  metadata: {
    ...revision.metadata,
    updatedAt: "2026-08-15T00:02:00Z",
    version: 2,
  },
  publishedAt: "2026-08-15T00:02:00Z",
  state: "published",
};

function renderWorkflow(
  selectedService?: AgentServiceResource,
  activeIsolationDomainId = isolationDomainId,
  selectedRevision?: ServiceRevisionResource,
  selectedPublishedRevision?: PublishedServiceRevisionResource,
): string {
  return renderToStaticMarkup(
    <AgentServiceAuthoringWorkflow
      canAssignAlias
      canCreateRevision
      canCreateService
      canPublishRevision
      client={{} as DataGroundClient}
      isolationDomainId={activeIsolationDomainId}
      onAssignAlias={() => undefined}
      onOpenRevision={() => undefined}
      onOpenService={() => undefined}
      selectedPublishedRevision={selectedPublishedRevision}
      selectedRevision={selectedRevision}
      selectedService={selectedService}
    />,
  );
}

describe("AgentServiceAuthoringWorkflow", () => {
  it("keeps revision drafting unavailable until a service is explicitly opened", () => {
    const markup = renderWorkflow();

    assert.match(markup, /Create agent service/u);
    assert.doesNotMatch(markup, /Create revision draft/u);
    assert.doesNotMatch(markup, /Publish revision/u);
    assert.doesNotMatch(markup, /Ready to assign/u);
  });

  it("opens revision drafting for the exact selected service and isolation scope", () => {
    const markup = renderWorkflow(service);

    assert.equal(isServiceSelectedForScope(service, isolationDomainId), true);
    assert.match(markup, /Create agent service/u);
    assert.match(markup, /Create revision draft/u);
    assert.match(markup, /svc_00000000000000000001/u);
    assert.match(markup, /reference\/v1/u);
    assert.doesNotMatch(markup, /Publish revision/u);
  });

  it("opens publication only for the explicitly selected draft", () => {
    const markup = renderWorkflow(service, isolationDomainId, revision);

    assert.equal(isRevisionSelectedForService(revision, service, isolationDomainId), true);
    assert.match(markup, /Create revision draft/u);
    assert.match(markup, /Publish revision/u);
    assert.match(markup, /rev_00000000000000000001/u);
    assert.doesNotMatch(markup, /Ready to assign/u);
  });

  it("opens alias routing only for the explicitly selected publication", () => {
    const markup = renderWorkflow(service, isolationDomainId, revision, publishedRevision);

    assert.equal(
      isPublishedRevisionSelectedForService(
        publishedRevision,
        revision,
        service,
        isolationDomainId,
      ),
      true,
    );
    assert.match(markup, /Publish revision/u);
    assert.match(markup, /Ready to assign/u);
    assert.match(markup, /value="stable"/u);
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
    assert.doesNotMatch(markup, /Publish revision/u);
    assert.doesNotMatch(markup, /Ready to assign/u);
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

  it("fails closed when selected revision state crosses the active service scope", () => {
    const crossServiceRevision: ServiceRevisionResource = {
      ...revision,
      serviceId: "svc_00000000000000000002",
    };
    const markup = renderWorkflow(service, isolationDomainId, crossServiceRevision);

    assert.equal(
      isRevisionSelectedForService(crossServiceRevision, service, isolationDomainId),
      false,
    );
    assert.match(markup, /Revision publication unavailable/u);
    assert.doesNotMatch(markup, /Publish revision/u);
    assert.doesNotMatch(markup, /rev_00000000000000000001/u);
    assert.doesNotMatch(markup, /svc_00000000000000000002/u);
  });

  it("does not disclose a selected revision from another isolation scope", () => {
    const crossScopeRevision: ServiceRevisionResource = {
      ...revision,
      metadata: {
        ...revision.metadata,
        isolationDomainId: "iso_00000000000000000002",
      },
    };
    const markup = renderWorkflow(service, isolationDomainId, crossScopeRevision);

    assert.equal(
      isRevisionSelectedForService(crossScopeRevision, service, isolationDomainId),
      false,
    );
    assert.match(markup, /Revision publication unavailable/u);
    assert.doesNotMatch(markup, /Publish revision/u);
    assert.doesNotMatch(markup, /rev_00000000000000000001/u);
    assert.doesNotMatch(markup, /iso_00000000000000000002/u);
  });

  it("rejects malformed revision definitions before exposing publication", () => {
    const malformedRevision: ServiceRevisionResource = {
      ...revision,
      runtimeProfile: " reference/v1",
    };
    const markup = renderWorkflow(service, isolationDomainId, malformedRevision);

    assert.equal(
      isRevisionSelectedForService(malformedRevision, service, isolationDomainId),
      false,
    );
    assert.match(markup, /Revision publication unavailable/u);
    assert.doesNotMatch(markup, /Publish revision/u);
  });

  it("rejects a selected revision without an active service", () => {
    const markup = renderWorkflow(undefined, isolationDomainId, revision);

    assert.match(markup, /Revision publication unavailable/u);
    assert.doesNotMatch(markup, /Publish revision/u);
    assert.doesNotMatch(markup, /rev_00000000000000000001/u);
  });

  it("does not disclose a selected publication from another isolation scope", () => {
    const crossScopePublication: PublishedServiceRevisionResource = {
      ...publishedRevision,
      metadata: {
        ...publishedRevision.metadata,
        isolationDomainId: "iso_00000000000000000002",
      },
    };
    const markup = renderWorkflow(service, isolationDomainId, revision, crossScopePublication);

    assert.equal(
      isPublishedRevisionSelectedForService(
        crossScopePublication,
        revision,
        service,
        isolationDomainId,
      ),
      false,
    );
    assert.match(markup, /Alias routing unavailable/u);
    assert.doesNotMatch(markup, /Ready to assign/u);
    assert.doesNotMatch(markup, /iso_00000000000000000002/u);
  });

  it("does not expose routing for a publication bound to another service", () => {
    const crossServicePublication: PublishedServiceRevisionResource = {
      ...publishedRevision,
      serviceId: "svc_00000000000000000002",
    };
    const markup = renderWorkflow(service, isolationDomainId, revision, crossServicePublication);

    assert.equal(
      isPublishedRevisionSelectedForService(
        crossServicePublication,
        revision,
        service,
        isolationDomainId,
      ),
      false,
    );
    assert.match(markup, /Alias routing unavailable/u);
    assert.doesNotMatch(markup, /Ready to assign/u);
    assert.doesNotMatch(markup, /svc_00000000000000000002/u);
  });

  it("rejects a substituted publication definition before exposing routing", () => {
    const substitutedPublication: PublishedServiceRevisionResource = {
      ...publishedRevision,
      runtimeProfile: "other/v1",
    };
    const markup = renderWorkflow(service, isolationDomainId, revision, substitutedPublication);

    assert.equal(
      isPublishedRevisionSelectedForService(
        substitutedPublication,
        revision,
        service,
        isolationDomainId,
      ),
      false,
    );
    assert.match(markup, /Alias routing unavailable/u);
    assert.doesNotMatch(markup, /Ready to assign/u);
    assert.doesNotMatch(markup, /other\/v1/u);
  });

  it("rejects a selected publication without its confirmed draft", () => {
    const markup = renderWorkflow(service, isolationDomainId, undefined, publishedRevision);

    assert.match(markup, /Alias routing unavailable/u);
    assert.doesNotMatch(markup, /Ready to assign/u);
    assert.doesNotMatch(markup, /rev_00000000000000000001/u);
  });
});
