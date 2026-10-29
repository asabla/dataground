import { isServiceAliasRoutedToRevision, type ServiceAliasResource } from "../aliases";
import {
  isPublishableServiceRevision,
  isPublishedServiceRevisionForDraft,
  type PublishedServiceRevisionResource,
  type ServiceRevisionResource,
} from "../revisions";
import type { AgentServiceResource } from "./client";

const serviceIdPattern = /^svc_[0-9a-z]{20,32}$/u;
const isolationDomainIdPattern = /^iso_[0-9a-z]{20,32}$/u;

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
