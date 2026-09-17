import type { DataGroundClient } from "../contracts/client";
import type { components } from "../contracts/openapi.gen";

export interface ResourceAuditReference {
  isolationDomainId: string;
  resourceType: "service-revision" | "invocation";
  resourceId: string;
}
export type ResourceAuditPage = components["schemas"]["ResourceAuditPage"];
export type ResourceAuditRecord = components["schemas"]["ResourceAuditRecord"];
export interface ResourceAuditFailure {
  code: string;
  message: string;
  correlationId?: string;
}
export type ResourceAuditRead =
  | { ok: true; page: ResourceAuditPage }
  | { ok: false; error: ResourceAuditFailure };

const scopePattern = /^iso_[0-9a-z]{20,32}$/u;
const receiptPattern = /^arr_[0-9a-z]{20,32}$/u;
const operationPattern = /^op_[0-9a-z]{20,32}$/u;
const vocabularyPattern = /^[a-z][a-z0-9.-]{0,127}$/u;
const timestampPattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/u;
const limit = 50;
function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
function text(value: unknown, maximum: number, pattern?: RegExp): value is string {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    new TextEncoder().encode(value).byteLength <= maximum &&
    !/[\p{Cc}\p{Cs}]/u.test(value) &&
    (pattern === undefined || pattern.test(value))
  );
}
function exactKeys(value: Record<string, unknown>, allowed: string[]) {
  return Object.keys(value).every((key) => allowed.includes(key));
}
export function validResourceAuditReference(value: ResourceAuditReference): boolean {
  return (
    scopePattern.test(value.isolationDomainId) &&
    ((value.resourceType === "invocation" && /^inv_[0-9a-z]{20,32}$/u.test(value.resourceId)) ||
      (value.resourceType === "service-revision" &&
        /^rev_[0-9a-z]{20,32}$/u.test(value.resourceId)))
  );
}
function decodeRecord(
  value: unknown,
  kind: ResourceAuditReference["resourceType"],
): value is ResourceAuditRecord {
  if (
    !record(value) ||
    !exactKeys(value, [
      "id",
      "source",
      "recordedAt",
      "actorId",
      "action",
      "outcome",
      "correlationId",
      "operationId",
      "policySetId",
      "policyDigest",
      "phase",
    ]) ||
    !text(value.recordedAt, 40, timestampPattern) ||
    !Number.isFinite(Date.parse(value.recordedAt)) ||
    !text(value.actorId, 256) ||
    !text(value.action, 128, vocabularyPattern) ||
    !text(value.correlationId, 256) ||
    (value.operationId !== undefined && !text(value.operationId, 35, operationPattern))
  )
    return false;
  if (value.source === "lifecycle")
    return (
      text(value.id, 36, /^aud_[0-9a-z]{20,32}$/u) &&
      ["accepted", "succeeded", "failed", "cancelled", "denied"].includes(
        value.outcome as string,
      ) &&
      value.policySetId === undefined &&
      value.policyDigest === undefined &&
      value.phase === undefined
    );
  if (
    !text(value.id, 36, /^ard_[0-9a-f]{32}$/u) ||
    !text(value.policySetId, 128) ||
    !text(value.policyDigest, 71, /^sha256:[0-9a-f]{64}$/u) ||
    !text(value.operationId, 35, operationPattern) ||
    !["allowed", "denied", "unavailable"].includes(value.outcome as string)
  )
    return false;
  if (kind === "service-revision")
    return (
      value.source === "publication-authorization" &&
      value.action === "publish" &&
      (value.phase === "entry" || value.phase === "effect")
    );
  return value.source === "invocation-authorization" && value.phase === undefined;
}
function decodePage(
  value: unknown,
  reference: ResourceAuditReference,
  cursor?: string,
): ResourceAuditPage | undefined {
  if (
    !record(value) ||
    !exactKeys(value, [
      "schemaVersion",
      "isolationDomainId",
      "resourceType",
      "resourceId",
      "receiptId",
      "items",
      "nextCursor",
    ]) ||
    value.schemaVersion !== "dataground.resource-audit-page/v1" ||
    value.isolationDomainId !== reference.isolationDomainId ||
    value.resourceType !== reference.resourceType ||
    value.resourceId !== reference.resourceId ||
    !text(value.receiptId, 36, receiptPattern) ||
    value.receiptId === cursor ||
    !Array.isArray(value.items) ||
    value.items.length > limit ||
    (value.nextCursor !== undefined &&
      (value.nextCursor !== value.receiptId || value.items.length !== limit))
  )
    return undefined;
  const ids = new Set<string>();
  let authorizationSeen = false;
  for (const item of value.items) {
    if (
      !decodeRecord(item, reference.resourceType) ||
      ids.has(item.id) ||
      (authorizationSeen && item.source === "lifecycle")
    )
      return undefined;
    ids.add(item.id);
    authorizationSeen ||= item.source !== "lifecycle";
  }
  return value as unknown as ResourceAuditPage;
}
const invalid = (): ResourceAuditRead => ({
  ok: false,
  error: {
    code: "WORKBENCH_AUDIT_RESPONSE_INVALID",
    message: "The audit response could not be verified. Refresh the audit to try again.",
  },
});
function failure(status: number, value: unknown): ResourceAuditRead {
  const correlationId =
    record(value) &&
    record(value.error) &&
    text(value.error.correlationId, 36, /^cor_[0-9a-z]{20,32}$/u)
      ? value.error.correlationId
      : undefined;
  const [code, message] =
    status === 401
      ? ["UNAUTHENTICATED", "Reconnect before reading audit records."]
      : status === 403
        ? ["ACTION_FORBIDDEN", "You do not have access to this resource's audit records."]
        : status === 404
          ? ["RESOURCE_NOT_FOUND", "The audit resource was not found."]
          : status === 400
            ? [
                "INVALID_REQUEST",
                "The audit cursor is no longer usable. Refresh the audit to start a new read.",
              ]
            : [
                "RESOURCE_AUDIT_UNAVAILABLE",
                "Audit records are unavailable for this connection. Try again later.",
              ];
  return { ok: false, error: { code, message, correlationId } };
}
export async function readResourceAudit(
  client: DataGroundClient,
  reference: ResourceAuditReference,
  cursor?: string,
): Promise<ResourceAuditRead> {
  if (
    !validResourceAuditReference(reference) ||
    (cursor !== undefined && !receiptPattern.test(cursor))
  )
    return invalid();
  try {
    const result =
      reference.resourceType === "invocation"
        ? await client.GET(
            "/v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/audit",
            {
              params: {
                path: {
                  isolationDomainId: reference.isolationDomainId,
                  invocationId: reference.resourceId,
                },
                query: { limit, cursor },
              },
              cache: "no-store",
            },
          )
        : await client.GET(
            "/v1/isolation-domains/{isolationDomainId}/service-revisions/{revisionId}/audit",
            {
              params: {
                path: {
                  isolationDomainId: reference.isolationDomainId,
                  revisionId: reference.resourceId,
                },
                query: { limit, cursor },
              },
              cache: "no-store",
            },
          );
    if (result.response.status !== 200) return failure(result.response.status, result.error);
    const page = decodePage(result.data, reference, cursor);
    return page === undefined ? invalid() : { ok: true, page };
  } catch {
    return {
      ok: false,
      error: {
        code: "WORKBENCH_AUDIT_UNAVAILABLE",
        message: "Audit records could not be read. Check the connection and try again.",
      },
    };
  }
}
