import type { ResourceAuditPage, ResourceAuditReference } from "./client";
export const reference: ResourceAuditReference = {
  isolationDomainId: "iso_00000000000000000001",
  resourceType: "invocation",
  resourceId: "inv_00000000000000000001",
};
export function auditPage(
  scope = reference,
  offset = 1,
  count = 1,
  more = false,
): ResourceAuditPage {
  const receiptId = `arr_${offset.toString().padStart(20, "0")}`;
  return {
    ...scope,
    schemaVersion: "dataground.resource-audit-page/v1",
    receiptId,
    items: Array.from({ length: count }, (_, i) => ({
      id: `aud_${(offset + i).toString().padStart(20, "0")}`,
      source: "lifecycle",
      recordedAt: "2026-09-17T12:00:00Z",
      actorId: `private-actor-${offset + i}`,
      action:
        scope.resourceType === "invocation" ? "invocation.accepted" : "service-revision.created",
      outcome: "accepted",
      correlationId: "cor_00000000000000000001",
    })),
    ...(more ? { nextCursor: receiptId } : {}),
  };
}
