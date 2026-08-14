import { StatusBadge } from "@dataground/ui";
import { useId } from "react";
import type { DataGroundClient } from "../contracts/client";
import { ServiceRevisionDraftWorkflow } from "../revisions";
import { AgentServiceCreateWorkflow } from "./AgentServiceCreateWorkflow";
import type { AgentServiceResource } from "./client";

const serviceIdPattern = /^svc_[0-9a-z]{20,32}$/u;
const isolationDomainIdPattern = /^iso_[0-9a-z]{20,32}$/u;

export interface AgentServiceAuthoringWorkflowProps {
  canCreateRevision: boolean;
  canCreateService: boolean;
  client: DataGroundClient;
  isolationDomainId: string;
  onOpenService: (service: AgentServiceResource) => void;
  revisionDisabledReason?: string;
  selectedService?: AgentServiceResource;
  serviceDisabledReason?: string;
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
  client,
  isolationDomainId,
  onOpenService,
  revisionDisabledReason,
  selectedService,
  serviceDisabledReason,
}: AgentServiceAuthoringWorkflowProps) {
  const blockedTitleId = useId();
  const serviceInScope =
    selectedService === undefined || isServiceSelectedForScope(selectedService, isolationDomainId);

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
            serviceId={selectedService.metadata.id}
          />
        ) : (
          <section
            aria-labelledby={blockedTitleId}
            className="product-workflow__blocked"
            role="alert"
          >
            <StatusBadge tone="critical">Scope mismatch</StatusBadge>
            <h2 id={blockedTitleId}>Revision drafting unavailable</h2>
            <p>
              The selected service does not belong to the active isolation scope. Disconnect before
              continuing with another scope.
            </p>
          </section>
        ))}
    </>
  );
}
