import { StatusBadge } from "@dataground/ui";
import { useId } from "react";
import type { DataGroundClient } from "../contracts/client";
import {
  isPublishableServiceRevision,
  ServiceRevisionDraftWorkflow,
  ServiceRevisionPublishWorkflow,
  type ServiceRevisionResource,
} from "../revisions";
import { AgentServiceCreateWorkflow } from "./AgentServiceCreateWorkflow";
import type { AgentServiceResource } from "./client";

const serviceIdPattern = /^svc_[0-9a-z]{20,32}$/u;
const isolationDomainIdPattern = /^iso_[0-9a-z]{20,32}$/u;

export interface AgentServiceAuthoringWorkflowProps {
  canCreateRevision: boolean;
  canCreateService: boolean;
  canPublishRevision: boolean;
  client: DataGroundClient;
  isolationDomainId: string;
  onOpenRevision: (revision: ServiceRevisionResource) => void;
  onOpenService: (service: AgentServiceResource) => void;
  publicationDisabledReason?: string;
  revisionDisabledReason?: string;
  selectedRevision?: ServiceRevisionResource;
  selectedService?: AgentServiceResource;
  serviceDisabledReason?: string;
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
  canCreateRevision,
  canCreateService,
  canPublishRevision,
  client,
  isolationDomainId,
  onOpenRevision,
  onOpenService,
  publicationDisabledReason,
  revisionDisabledReason,
  selectedRevision,
  selectedService,
  serviceDisabledReason,
}: AgentServiceAuthoringWorkflowProps) {
  const draftingBlockedTitleId = useId();
  const publicationBlockedTitleId = useId();
  const serviceInScope =
    selectedService === undefined || isServiceSelectedForScope(selectedService, isolationDomainId);
  const revisionInScope =
    selectedRevision !== undefined &&
    selectedService !== undefined &&
    isRevisionSelectedForService(selectedRevision, selectedService, isolationDomainId);

  return (
    <>
      <AgentServiceCreateWorkflow
        canCreate={canCreateService}
        client={client}
        disabledReason={serviceDisabledReason}
        isolationDomainId={isolationDomainId}
        onOpenService={onOpenService}
      />

      {selectedService &&
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
        serviceInScope &&
        (selectedService && revisionInScope ? (
          <ServiceRevisionPublishWorkflow
            canPublish={canPublishRevision}
            client={client}
            disabledReason={publicationDisabledReason}
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
    </>
  );
}
