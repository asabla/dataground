import { Button, StatusBadge } from "@dataground/ui";
import { useId } from "react";
import {
  isServiceAliasRoutedToRevision,
  ServiceAliasAssignWorkflow,
  type ServiceAliasResource,
} from "../aliases";
import { ApprovalWorkflow, type InvocationApprovalReference } from "../approvals";
import { ArtifactWorkflow, type InvocationArtifactReference } from "../artifacts";
import type { DataGroundClient } from "../contracts/client";
import { EventTimelineWorkflow } from "../events";
import {
  InvocationComposerWorkflow,
  type InvocationReference,
  InvocationWorkflow,
} from "../invocations";
import {
  isPublishableServiceRevision,
  isPublishedServiceRevisionForDraft,
  type PublishedServiceRevisionResource,
  ServiceRevisionDraftWorkflow,
  ServiceRevisionPublishWorkflow,
  type ServiceRevisionResource,
} from "../revisions";
import { AgentServiceCreateWorkflow } from "./AgentServiceCreateWorkflow";
import type { AgentServiceResource } from "./client";

const serviceIdPattern = /^svc_[0-9a-z]{20,32}$/u;
const isolationDomainIdPattern = /^iso_[0-9a-z]{20,32}$/u;
const invocationIdPattern = /^inv_[0-9a-z]{20,32}$/u;
const approvalIdPattern = /^apr_[0-9a-z]{20,32}$/u;
const artifactIdPattern = /^art_[0-9a-z]{20,32}$/u;

export interface AgentServiceInvocationSelection {
  aliasGeneration: number;
  aliasId: string;
  aliasVersion: number;
  reference: InvocationReference;
}

export interface AgentServiceAuthoringWorkflowProps {
  aliasDisabledReason?: string;
  canAssignAlias: boolean;
  canCancelInvocation: boolean;
  canCreateRevision: boolean;
  canCreateService: boolean;
  canInvokeService: boolean;
  canPublishRevision: boolean;
  canResolveApproval: boolean;
  client: DataGroundClient;
  cancellationDisabledReason?: string;
  isolationDomainId: string;
  invocationDisabledReason?: string;
  onAssignAlias: (revision: PublishedServiceRevisionResource) => void;
  onCloseApproval: () => void;
  onCloseArtifact: () => void;
  onComposeInvocation: (alias: ServiceAliasResource) => void;
  onInspectApproval: (reference: InvocationApprovalReference) => void;
  onInspectArtifact: (reference: InvocationArtifactReference) => void;
  onOpenInvocation: (selection: AgentServiceInvocationSelection) => void;
  onOpenRevision: (revision: ServiceRevisionResource) => void;
  onOpenService: (service: AgentServiceResource) => void;
  publicationDisabledReason?: string;
  revisionDisabledReason?: string;
  selectedAlias?: ServiceAliasResource;
  selectedApproval?: InvocationApprovalReference;
  selectedArtifact?: InvocationArtifactReference;
  selectedInvocation?: AgentServiceInvocationSelection;
  selectedPublishedRevision?: PublishedServiceRevisionResource;
  selectedRevision?: ServiceRevisionResource;
  selectedService?: AgentServiceResource;
  serviceDisabledReason?: string;
  focusCurrentStage?: boolean;
}

export function isInvocationSelectedForAlias(
  selection: AgentServiceInvocationSelection,
  alias: ServiceAliasResource,
  isolationDomainId: string,
): boolean {
  return (
    isolationDomainIdPattern.test(isolationDomainId) &&
    invocationIdPattern.test(selection.reference.invocationId) &&
    selection.reference.isolationDomainId === isolationDomainId &&
    alias.metadata.isolationDomainId === isolationDomainId &&
    selection.aliasId === alias.metadata.id &&
    selection.aliasGeneration === alias.metadata.generation &&
    selection.aliasVersion === alias.metadata.version
  );
}

export function isArtifactSelectedForInvocation(
  artifact: InvocationArtifactReference,
  invocation: InvocationReference,
): boolean {
  return (
    isolationDomainIdPattern.test(invocation.isolationDomainId) &&
    invocationIdPattern.test(invocation.invocationId) &&
    artifactIdPattern.test(artifact.artifactId) &&
    artifact.isolationDomainId === invocation.isolationDomainId &&
    artifact.invocationId === invocation.invocationId
  );
}

export function isApprovalSelectedForInvocation(
  approval: InvocationApprovalReference,
  invocation: InvocationReference,
): boolean {
  return (
    isolationDomainIdPattern.test(invocation.isolationDomainId) &&
    invocationIdPattern.test(invocation.invocationId) &&
    approvalIdPattern.test(approval.approvalId) &&
    approval.isolationDomainId === invocation.isolationDomainId &&
    approval.invocationId === invocation.invocationId
  );
}

export function isAliasSelectedForPublishedRevision(
  alias: ServiceAliasResource,
  publishedRevision: PublishedServiceRevisionResource,
  revision: ServiceRevisionResource,
  service: AgentServiceResource,
  isolationDomainId: string,
): boolean {
  return (
    isPublishedRevisionSelectedForService(
      publishedRevision,
      revision,
      service,
      isolationDomainId,
    ) && isServiceAliasRoutedToRevision(alias, publishedRevision)
  );
}

export function isPublishedRevisionSelectedForService(
  publishedRevision: PublishedServiceRevisionResource,
  revision: ServiceRevisionResource,
  service: AgentServiceResource,
  isolationDomainId: string,
): boolean {
  return (
    isRevisionSelectedForService(revision, service, isolationDomainId) &&
    isPublishedServiceRevisionForDraft(publishedRevision, revision)
  );
}

export function isRevisionSelectedForService(
  revision: ServiceRevisionResource,
  service: AgentServiceResource,
  isolationDomainId: string,
): boolean {
  return (
    isServiceSelectedForScope(service, isolationDomainId) &&
    revision.metadata.isolationDomainId === isolationDomainId &&
    revision.serviceId === service.metadata.id &&
    isPublishableServiceRevision(revision)
  );
}

export function isServiceSelectedForScope(
  service: AgentServiceResource,
  isolationDomainId: string,
): boolean {
  return (
    isolationDomainIdPattern.test(isolationDomainId) &&
    service.metadata.isolationDomainId === isolationDomainId &&
    serviceIdPattern.test(service.metadata.id)
  );
}

export function AgentServiceAuthoringWorkflow({
  aliasDisabledReason,
  canAssignAlias,
  canCancelInvocation,
  canCreateRevision,
  canCreateService,
  canInvokeService,
  canPublishRevision,
  canResolveApproval,
  cancellationDisabledReason,
  client,
  isolationDomainId,
  invocationDisabledReason,
  onAssignAlias,
  onCloseApproval,
  onCloseArtifact,
  onComposeInvocation,
  onInspectApproval,
  onInspectArtifact,
  onOpenInvocation,
  onOpenRevision,
  onOpenService,
  publicationDisabledReason,
  revisionDisabledReason,
  selectedAlias,
  selectedApproval,
  selectedArtifact,
  selectedInvocation,
  selectedPublishedRevision,
  selectedRevision,
  selectedService,
  serviceDisabledReason,
  focusCurrentStage = false,
}: AgentServiceAuthoringWorkflowProps) {
  const aliasBlockedTitleId = useId();
  const draftingBlockedTitleId = useId();
  const publicationBlockedTitleId = useId();
  const invocationBlockedTitleId = useId();
  const monitoringBlockedTitleId = useId();
  const approvalBlockedTitleId = useId();
  const approvalInspectionTitleId = useId();
  const artifactBlockedTitleId = useId();
  const artifactInspectionTitleId = useId();
  const serviceInScope =
    selectedService === undefined || isServiceSelectedForScope(selectedService, isolationDomainId);
  const revisionInScope =
    selectedRevision !== undefined &&
    selectedService !== undefined &&
    isRevisionSelectedForService(selectedRevision, selectedService, isolationDomainId);
  const publishedRevisionInScope =
    selectedPublishedRevision !== undefined &&
    selectedRevision !== undefined &&
    selectedService !== undefined &&
    isPublishedRevisionSelectedForService(
      selectedPublishedRevision,
      selectedRevision,
      selectedService,
      isolationDomainId,
    );
  const aliasInScope =
    selectedAlias !== undefined &&
    selectedPublishedRevision !== undefined &&
    selectedRevision !== undefined &&
    selectedService !== undefined &&
    isAliasSelectedForPublishedRevision(
      selectedAlias,
      selectedPublishedRevision,
      selectedRevision,
      selectedService,
      isolationDomainId,
    );
  const activeStage = selectedInvocation
    ? "monitoring"
    : selectedAlias
      ? "invocation"
      : selectedPublishedRevision
        ? "alias"
        : selectedRevision
          ? "publication"
          : selectedService
            ? "revision"
            : "service";

  return (
    <>
      {(!focusCurrentStage || activeStage === "service") && (
        <AgentServiceCreateWorkflow
          canCreate={canCreateService}
          client={client}
          disabledReason={serviceDisabledReason}
          isolationDomainId={isolationDomainId}
          onOpenService={onOpenService}
        />
      )}

      {selectedService &&
        (!focusCurrentStage || activeStage === "revision") &&
        (serviceInScope ? (
          <ServiceRevisionDraftWorkflow
            canCreateRevision={canCreateRevision}
            client={client}
            disabledReason={revisionDisabledReason}
            isolationDomainId={isolationDomainId}
            onOpenRevision={onOpenRevision}
            serviceId={selectedService.metadata.id}
          />
        ) : (
          <section
            aria-labelledby={draftingBlockedTitleId}
            className="product-workflow__blocked"
            role="alert"
          >
            <StatusBadge tone="critical">Scope mismatch</StatusBadge>
            <h2 id={draftingBlockedTitleId}>Revision drafting unavailable</h2>
            <p>
              The selected service does not belong to the active isolation scope. Disconnect before
              continuing with another scope.
            </p>
          </section>
        ))}

      {selectedRevision &&
        (!focusCurrentStage || activeStage === "publication") &&
        (selectedService && revisionInScope ? (
          <ServiceRevisionPublishWorkflow
            canPublish={canPublishRevision}
            client={client}
            disabledReason={publicationDisabledReason}
            onAssignAlias={onAssignAlias}
            revision={selectedRevision}
          />
        ) : (
          <section
            aria-labelledby={publicationBlockedTitleId}
            className="product-workflow__blocked"
            role="alert"
          >
            <StatusBadge tone="critical">Scope mismatch</StatusBadge>
            <h2 id={publicationBlockedTitleId}>Revision publication unavailable</h2>
            <p>
              The selected revision is not a publishable draft for the active service and isolation
              scope. Reopen the exact service and draft before continuing.
            </p>
          </section>
        ))}

      {selectedPublishedRevision &&
        (!focusCurrentStage || activeStage === "alias") &&
        (selectedService && selectedRevision && publishedRevisionInScope ? (
          <ServiceAliasAssignWorkflow
            canAssign={canAssignAlias}
            client={client}
            disabledReason={aliasDisabledReason}
            onComposeInvocation={onComposeInvocation}
            revision={selectedPublishedRevision}
          />
        ) : (
          <section
            aria-labelledby={aliasBlockedTitleId}
            className="product-workflow__blocked"
            role="alert"
          >
            <StatusBadge tone="critical">Scope mismatch</StatusBadge>
            <h2 id={aliasBlockedTitleId}>Alias routing unavailable</h2>
            <p>
              The selected publication is not the confirmed result of the active service draft.
              Reopen the exact service, draft, and publication before changing routing.
            </p>
          </section>
        ))}

      {selectedAlias &&
        (!focusCurrentStage || activeStage === "invocation") &&
        (selectedService && selectedRevision && selectedPublishedRevision && aliasInScope ? (
          <InvocationComposerWorkflow
            canInvoke={canInvokeService}
            client={client}
            disabledReason={invocationDisabledReason}
            initialAlias={selectedAlias.name}
            inputSchema={selectedPublishedRevision.inputSchema}
            onOpenInvocation={(reference) =>
              onOpenInvocation({
                aliasGeneration: selectedAlias.metadata.generation,
                aliasId: selectedAlias.metadata.id,
                aliasVersion: selectedAlias.metadata.version,
                reference,
              })
            }
            target={{ isolationDomainId, serviceId: selectedService.metadata.id }}
          />
        ) : (
          <section
            aria-labelledby={invocationBlockedTitleId}
            className="product-workflow__blocked"
            role="alert"
          >
            <StatusBadge tone="critical">Scope mismatch</StatusBadge>
            <h2 id={invocationBlockedTitleId}>Invocation composition unavailable</h2>
            <p>
              The selected alias is not the confirmed route for the active publication. Reopen the
              exact service, draft, publication, and alias before composing an invocation.
            </p>
          </section>
        ))}

      {selectedInvocation &&
        (!focusCurrentStage || activeStage === "monitoring") &&
        (selectedService &&
        selectedRevision &&
        selectedPublishedRevision &&
        selectedAlias &&
        aliasInScope &&
        isInvocationSelectedForAlias(selectedInvocation, selectedAlias, isolationDomainId) ? (
          <>
            <InvocationWorkflow
              canCancel={canCancelInvocation}
              client={client}
              disabledReason={cancellationDisabledReason}
              reference={selectedInvocation.reference}
            />
            <EventTimelineWorkflow
              client={client}
              onInspectApproval={onInspectApproval}
              onInspectArtifact={onInspectArtifact}
              reference={selectedInvocation.reference}
            />
            {selectedApproval &&
              (isApprovalSelectedForInvocation(selectedApproval, selectedInvocation.reference) ? (
                <section
                  aria-labelledby={approvalInspectionTitleId}
                  className="product-workflow__inspection"
                >
                  <div className="product-workflow__inspection-heading">
                    <div>
                      <p className="workbench-kicker">Runtime decision</p>
                      <h2 id={approvalInspectionTitleId}>Approval request</h2>
                    </div>
                    <Button onPress={onCloseApproval} variant="quiet">
                      Close approval
                    </Button>
                  </div>
                  <ApprovalWorkflow
                    canResolve={canResolveApproval}
                    client={client}
                    reference={selectedApproval}
                  />
                </section>
              ) : (
                <section
                  aria-labelledby={approvalBlockedTitleId}
                  className="product-workflow__blocked"
                  role="alert"
                >
                  <StatusBadge tone="critical">Scope mismatch</StatusBadge>
                  <h2 id={approvalBlockedTitleId}>Approval review unavailable</h2>
                  <p>
                    The selected approval does not belong to the active invocation. Reopen it from
                    the confirmed event timeline before reviewing the request.
                  </p>
                </section>
              ))}
            {selectedArtifact &&
              (isArtifactSelectedForInvocation(selectedArtifact, selectedInvocation.reference) ? (
                <section
                  aria-labelledby={artifactInspectionTitleId}
                  className="product-workflow__inspection"
                >
                  <div className="product-workflow__inspection-heading">
                    <div>
                      <p className="workbench-kicker">Governed output</p>
                      <h2 id={artifactInspectionTitleId}>Artifact inspection</h2>
                    </div>
                    <Button onPress={onCloseArtifact} variant="quiet">
                      Close metadata
                    </Button>
                  </div>
                  <ArtifactWorkflow client={client} reference={selectedArtifact} />
                </section>
              ) : (
                <section
                  aria-labelledby={artifactBlockedTitleId}
                  className="product-workflow__blocked"
                  role="alert"
                >
                  <StatusBadge tone="critical">Scope mismatch</StatusBadge>
                  <h2 id={artifactBlockedTitleId}>Artifact inspection unavailable</h2>
                  <p>
                    The selected artifact does not belong to the active invocation. Reopen it from
                    the confirmed event timeline before inspecting metadata.
                  </p>
                </section>
              ))}
          </>
        ) : (
          <section
            aria-labelledby={monitoringBlockedTitleId}
            className="product-workflow__blocked"
            role="alert"
          >
            <StatusBadge tone="critical">Scope mismatch</StatusBadge>
            <h2 id={monitoringBlockedTitleId}>Invocation monitoring unavailable</h2>
            <p>
              The selected invocation was not accepted for the active alias route. Reopen the exact
              service, draft, publication, alias, and invocation before monitoring lifecycle state.
            </p>
          </section>
        ))}
    </>
  );
}
