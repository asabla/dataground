import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import type { ServiceAliasResource } from "../aliases";
import type { InvocationApprovalReference } from "../approvals";
import type { InvocationArtifactReference } from "../artifacts";
import type { DataGroundClient } from "../contracts/client";
import type { InvocationReference } from "../invocations";
import type { PublishedServiceRevisionResource, ServiceRevisionResource } from "../revisions";
import {
  AgentServiceAuthoringWorkflow,
  type AgentServiceInvocationSelection,
  isAliasSelectedForPublishedRevision,
  isApprovalSelectedForInvocation,
  isArtifactSelectedForInvocation,
  isInvocationSelectedForAlias,
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
  inputSchema: {
    additionalProperties: false,
    properties: { prompt: { maxLength: 262_144, minLength: 1, type: "string" } },
    required: ["prompt"],
    type: "object",
  },
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
const alias: ServiceAliasResource = {
  metadata: {
    createdAt: "2026-08-15T00:03:00Z",
    createdBy: "usr_00000000000000000001",
    generation: 1,
    id: "als_00000000000000000001",
    isolationDomainId,
    updatedAt: "2026-08-15T00:03:00Z",
    version: 1,
  },
  name: "stable",
  revisionId: publishedRevision.metadata.id,
  serviceId: service.metadata.id,
};
const invocationReference: InvocationReference = {
  invocationId: "inv_00000000000000000001",
  isolationDomainId,
};
const invocationSelection = {
  aliasGeneration: alias.metadata.generation,
  aliasId: alias.metadata.id,
  aliasVersion: alias.metadata.version,
  reference: invocationReference,
};
const artifactReference: InvocationArtifactReference = {
  artifactId: "art_00000000000000000001",
  ...invocationReference,
};
const approvalReference: InvocationApprovalReference = {
  approvalId: "apr_00000000000000000001",
  ...invocationReference,
};

function renderWorkflow(
  selectedService?: AgentServiceResource,
  activeIsolationDomainId = isolationDomainId,
  selectedRevision?: ServiceRevisionResource,
  selectedPublishedRevision?: PublishedServiceRevisionResource,
  selectedAlias?: ServiceAliasResource,
  selectedInvocation?: AgentServiceInvocationSelection,
  selectedArtifact?: InvocationArtifactReference,
  selectedApproval?: InvocationApprovalReference,
): string {
  return renderToStaticMarkup(
    <AgentServiceAuthoringWorkflow
      canAssignAlias
      canCancelInvocation
      canCreateRevision
      canCreateService
      canInvokeService
      canPublishRevision
      canResolveApproval
      client={{} as DataGroundClient}
      isolationDomainId={activeIsolationDomainId}
      onAssignAlias={() => undefined}
      onCloseApproval={() => undefined}
      onCloseArtifact={() => undefined}
      onComposeInvocation={() => undefined}
      onInspectApproval={() => undefined}
      onInspectArtifact={() => undefined}
      onOpenInvocation={() => undefined}
      onOpenRevision={() => undefined}
      onOpenService={() => undefined}
      selectedAlias={selectedAlias}
      selectedApproval={selectedApproval}
      selectedArtifact={selectedArtifact}
      selectedInvocation={selectedInvocation}
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
    assert.doesNotMatch(markup, /Ready to invoke/u);
    assert.doesNotMatch(markup, /Event timeline/u);
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
    assert.doesNotMatch(markup, /Ready to invoke/u);
  });

  it("opens invocation composition only for the explicitly selected route", () => {
    const markup = renderWorkflow(service, isolationDomainId, revision, publishedRevision, alias);

    assert.equal(
      isAliasSelectedForPublishedRevision(
        alias,
        publishedRevision,
        revision,
        service,
        isolationDomainId,
      ),
      true,
    );
    assert.match(markup, /Create invocation/u);
    assert.match(markup, /Ready to invoke/u);
    assert.match(markup, /name="prompt"/u);
    assert.doesNotMatch(markup, /Event timeline/u);
  });

  it("can focus the UI on only the current valid stage", () => {
    const markup = renderToStaticMarkup(
      <AgentServiceAuthoringWorkflow
        canAssignAlias
        canCancelInvocation
        canCreateRevision
        canCreateService
        canInvokeService
        canPublishRevision
        canResolveApproval
        client={{} as DataGroundClient}
        focusCurrentStage
        isolationDomainId={isolationDomainId}
        onAssignAlias={() => undefined}
        onCloseApproval={() => undefined}
        onCloseArtifact={() => undefined}
        onComposeInvocation={() => undefined}
        onInspectApproval={() => undefined}
        onInspectArtifact={() => undefined}
        onOpenInvocation={() => undefined}
        onOpenRevision={() => undefined}
        onOpenService={() => undefined}
        selectedAlias={alias}
        selectedPublishedRevision={publishedRevision}
        selectedRevision={revision}
        selectedService={service}
      />,
    );

    assert.match(markup, /Create invocation/u);
    assert.doesNotMatch(markup, /Create agent service/u);
    assert.doesNotMatch(markup, /Create revision draft/u);
    assert.doesNotMatch(markup, /Publish revision/u);
    assert.doesNotMatch(markup, /Ready to assign/u);
  });

  it("opens invocation observability only for the invocation accepted from the exact alias state", () => {
    const markup = renderWorkflow(
      service,
      isolationDomainId,
      revision,
      publishedRevision,
      alias,
      invocationSelection,
    );

    assert.equal(isInvocationSelectedForAlias(invocationSelection, alias, isolationDomainId), true);
    assert.match(markup, /Loading invocation/u);
    assert.match(markup, /Event timeline/u);
    assert.match(markup, /Replaying events/u);
    assert.match(markup, /inv_00000000000000000001/u);
    assert.doesNotMatch(markup, /Invocation monitoring unavailable/u);
  });

  it("opens governed artifact metadata only for the active invocation", () => {
    const markup = renderWorkflow(
      service,
      isolationDomainId,
      revision,
      publishedRevision,
      alias,
      invocationSelection,
      artifactReference,
    );

    assert.equal(isArtifactSelectedForInvocation(artifactReference, invocationReference), true);
    assert.match(markup, /Artifact inspection/u);
    assert.match(markup, /Loading metadata/u);
    assert.match(markup, /art_00000000000000000001/u);
    assert.match(markup, /Close metadata/u);
  });

  it("opens governed approval review only for the active invocation", () => {
    const markup = renderWorkflow(
      service,
      isolationDomainId,
      revision,
      publishedRevision,
      alias,
      invocationSelection,
      undefined,
      approvalReference,
    );

    assert.equal(isApprovalSelectedForInvocation(approvalReference, invocationReference), true);
    assert.match(markup, /Approval request/u);
    assert.match(markup, /Loading approval/u);
    assert.match(markup, /Close approval/u);
    assert.doesNotMatch(markup, /Approval review unavailable/u);
  });

  it("fails closed without disclosing an approval selected from another invocation", () => {
    const crossInvocationApproval: InvocationApprovalReference = {
      ...approvalReference,
      invocationId: "inv_00000000000000000002",
    };
    const markup = renderWorkflow(
      service,
      isolationDomainId,
      revision,
      publishedRevision,
      alias,
      invocationSelection,
      undefined,
      crossInvocationApproval,
    );

    assert.equal(
      isApprovalSelectedForInvocation(crossInvocationApproval, invocationReference),
      false,
    );
    assert.match(markup, /Approval review unavailable/u);
    assert.doesNotMatch(markup, /Loading approval/u);
    assert.doesNotMatch(markup, /inv_00000000000000000002/u);
    assert.doesNotMatch(markup, /apr_00000000000000000001/u);
  });

  it("fails closed without disclosing an artifact selected from another invocation", () => {
    const crossInvocationArtifact: InvocationArtifactReference = {
      ...artifactReference,
      invocationId: "inv_00000000000000000002",
    };
    const markup = renderWorkflow(
      service,
      isolationDomainId,
      revision,
      publishedRevision,
      alias,
      invocationSelection,
      crossInvocationArtifact,
    );

    assert.equal(
      isArtifactSelectedForInvocation(crossInvocationArtifact, invocationReference),
      false,
    );
    assert.match(markup, /Artifact inspection unavailable/u);
    assert.doesNotMatch(markup, /Loading metadata/u);
    assert.doesNotMatch(markup, /inv_00000000000000000002/u);
    assert.doesNotMatch(markup, /art_00000000000000000001/u);
  });

  it("fails closed when an invocation reference crosses the active isolation scope", () => {
    const crossScopeSelection: AgentServiceInvocationSelection = {
      ...invocationSelection,
      reference: {
        ...invocationReference,
        isolationDomainId: "iso_00000000000000000002",
      },
    };
    const markup = renderWorkflow(
      service,
      isolationDomainId,
      revision,
      publishedRevision,
      alias,
      crossScopeSelection,
    );

    assert.equal(
      isInvocationSelectedForAlias(crossScopeSelection, alias, isolationDomainId),
      false,
    );
    assert.match(markup, /Invocation monitoring unavailable/u);
    assert.doesNotMatch(markup, /Loading invocation/u);
    assert.doesNotMatch(markup, /Event timeline/u);
    assert.doesNotMatch(markup, /iso_00000000000000000002/u);
    assert.doesNotMatch(markup, /inv_00000000000000000001/u);
  });

  it("rejects malformed invocation references before lifecycle state is requested", () => {
    const malformedSelection: AgentServiceInvocationSelection = {
      ...invocationSelection,
      reference: { ...invocationReference, invocationId: "invalid" },
    };
    const markup = renderWorkflow(
      service,
      isolationDomainId,
      revision,
      publishedRevision,
      alias,
      malformedSelection,
    );

    assert.equal(isInvocationSelectedForAlias(malformedSelection, alias, isolationDomainId), false);
    assert.match(markup, /Invocation monitoring unavailable/u);
    assert.doesNotMatch(markup, /Loading invocation/u);
    assert.doesNotMatch(markup, /Event timeline/u);
  });

  it("rejects an invocation accepted before the selected alias changed", () => {
    const staleSelection: AgentServiceInvocationSelection = {
      ...invocationSelection,
      aliasVersion: alias.metadata.version + 1,
    };
    const markup = renderWorkflow(
      service,
      isolationDomainId,
      revision,
      publishedRevision,
      alias,
      staleSelection,
    );

    assert.equal(isInvocationSelectedForAlias(staleSelection, alias, isolationDomainId), false);
    assert.match(markup, /Invocation monitoring unavailable/u);
    assert.doesNotMatch(markup, /Loading invocation/u);
    assert.doesNotMatch(markup, /Event timeline/u);
    assert.doesNotMatch(markup, /inv_00000000000000000001/u);
  });

  it("keeps invocation composition unavailable for an unsupported input contract", () => {
    const noSchemaDraft: ServiceRevisionResource = { ...revision, inputSchema: undefined };
    const noSchemaPublication: PublishedServiceRevisionResource = {
      ...publishedRevision,
      inputSchema: undefined,
    };
    const markup = renderWorkflow(
      service,
      isolationDomainId,
      noSchemaDraft,
      noSchemaPublication,
      alias,
    );

    assert.match(markup, /Composer unavailable/u);
    assert.match(markup, /Input contract unavailable/u);
    assert.doesNotMatch(markup, /Ready to invoke/u);
    assert.doesNotMatch(markup, /Start invocation/u);
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
    assert.doesNotMatch(markup, /Ready to invoke/u);
    assert.doesNotMatch(markup, /iso_00000000000000000002/u);
    assert.doesNotMatch(markup, /svc_00000000000000000001/u);
  });

  it("keeps the focused stage explanatory when an upstream scope check fails", () => {
    const crossScopeService: AgentServiceResource = {
      ...service,
      metadata: {
        ...service.metadata,
        isolationDomainId: "iso_00000000000000000002",
      },
    };
    const markup = renderToStaticMarkup(
      <AgentServiceAuthoringWorkflow
        canAssignAlias
        canCancelInvocation
        canCreateRevision
        canCreateService
        canInvokeService
        canPublishRevision
        canResolveApproval
        client={{} as DataGroundClient}
        focusCurrentStage
        isolationDomainId={isolationDomainId}
        onAssignAlias={() => undefined}
        onCloseApproval={() => undefined}
        onCloseArtifact={() => undefined}
        onComposeInvocation={() => undefined}
        onInspectApproval={() => undefined}
        onInspectArtifact={() => undefined}
        onOpenInvocation={() => undefined}
        onOpenRevision={() => undefined}
        onOpenService={() => undefined}
        selectedRevision={revision}
        selectedService={crossScopeService}
      />,
    );

    assert.match(markup, /Revision publication unavailable/u);
    assert.doesNotMatch(markup, /Publish revision/u);
    assert.doesNotMatch(markup, /iso_00000000000000000002/u);
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

  it("does not disclose a selected alias from another isolation scope", () => {
    const crossScopeAlias: ServiceAliasResource = {
      ...alias,
      metadata: {
        ...alias.metadata,
        isolationDomainId: "iso_00000000000000000002",
      },
    };
    const markup = renderWorkflow(
      service,
      isolationDomainId,
      revision,
      publishedRevision,
      crossScopeAlias,
    );

    assert.equal(
      isAliasSelectedForPublishedRevision(
        crossScopeAlias,
        publishedRevision,
        revision,
        service,
        isolationDomainId,
      ),
      false,
    );
    assert.match(markup, /Invocation composition unavailable/u);
    assert.doesNotMatch(markup, /Ready to invoke/u);
    assert.doesNotMatch(markup, /iso_00000000000000000002/u);
  });

  it("does not expose composition for an alias bound to another service", () => {
    const crossServiceAlias: ServiceAliasResource = {
      ...alias,
      serviceId: "svc_00000000000000000002",
    };
    const markup = renderWorkflow(
      service,
      isolationDomainId,
      revision,
      publishedRevision,
      crossServiceAlias,
    );

    assert.equal(
      isAliasSelectedForPublishedRevision(
        crossServiceAlias,
        publishedRevision,
        revision,
        service,
        isolationDomainId,
      ),
      false,
    );
    assert.match(markup, /Invocation composition unavailable/u);
    assert.doesNotMatch(markup, /Ready to invoke/u);
    assert.doesNotMatch(markup, /svc_00000000000000000002/u);
  });

  it("rejects an alias routed to another revision before exposing composition", () => {
    const substitutedAlias: ServiceAliasResource = {
      ...alias,
      revisionId: "rev_00000000000000000002",
    };
    const markup = renderWorkflow(
      service,
      isolationDomainId,
      revision,
      publishedRevision,
      substitutedAlias,
    );

    assert.equal(
      isAliasSelectedForPublishedRevision(
        substitutedAlias,
        publishedRevision,
        revision,
        service,
        isolationDomainId,
      ),
      false,
    );
    assert.match(markup, /Invocation composition unavailable/u);
    assert.doesNotMatch(markup, /Ready to invoke/u);
    assert.doesNotMatch(markup, /rev_00000000000000000002/u);
  });

  it("rejects alias state that predates the selected publication", () => {
    const staleAlias: ServiceAliasResource = {
      ...alias,
      metadata: {
        ...alias.metadata,
        createdAt: "2026-08-15T00:01:30Z",
        updatedAt: "2026-08-15T00:01:30Z",
      },
    };
    const markup = renderWorkflow(
      service,
      isolationDomainId,
      revision,
      publishedRevision,
      staleAlias,
    );

    assert.equal(
      isAliasSelectedForPublishedRevision(
        staleAlias,
        publishedRevision,
        revision,
        service,
        isolationDomainId,
      ),
      false,
    );
    assert.match(markup, /Invocation composition unavailable/u);
    assert.doesNotMatch(markup, /Ready to invoke/u);
  });

  it("rejects a selected alias without its confirmed publication", () => {
    const markup = renderWorkflow(service, isolationDomainId, revision, undefined, alias);

    assert.match(markup, /Invocation composition unavailable/u);
    assert.doesNotMatch(markup, /Ready to invoke/u);
    assert.doesNotMatch(markup, /als_00000000000000000001/u);
  });
});
