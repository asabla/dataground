import type { DataGroundClient } from "../contracts/client";
import type { components } from "../contracts/openapi.gen";
import { compareTimestamps } from "../contracts/timestamps";
import { decodeResourceSummary, type SearchKind } from "../search/client";
import type { AgentServiceFailure } from "../services/client";

export type SavedResource = components["schemas"]["SavedResource"] & { metadata: { id: string } };
export type SavedResourceRequest = components["schemas"]["SetSavedResourceRequest"];
type Result<T> = ({ ok: true } & T) | { ok: false; error: AgentServiceFailure };
const root = "/v1/isolation-domains/{isolationDomainId}/saved-resources";
const scopePattern = /^iso_[0-9a-z]{20,32}$/u;
const cursorPattern = /^[A-Za-z0-9_-]{1,512}$/u;
const record = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" && v !== null && !Array.isArray(v);
const stamp = (v: unknown): v is string =>
  typeof v === "string" &&
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/u.test(v) &&
  Number.isFinite(Date.parse(v));
const prefixes: Record<SearchKind, string> = {
  workspace: "wsp",
  "agent-service": "svc",
  invocation: "inv",
  artifact: "art",
};
export function validSavedReference(kind: unknown, id: unknown): kind is SearchKind {
  return (
    typeof kind === "string" &&
    Object.hasOwn(prefixes, kind) &&
    typeof id === "string" &&
    /^(wsp|svc|inv|art)_[0-9a-z]{20,32}$/u.test(id) &&
    id.startsWith(`${prefixes[kind as SearchKind]}_`)
  );
}
function fail(
  code: string,
  message: string,
  retryable = false,
  outcomeUnknown = false,
): { ok: false; error: AgentServiceFailure } {
  return {
    ok: false,
    error: { code, message, retryable, ...(outcomeUnknown ? { outcomeUnknown: true } : {}) },
  };
}
function responseFailure(
  error: unknown,
  status: number,
  mutation = false,
): { ok: false; error: AgentServiceFailure } {
  const unknown = mutation && status >= 500;
  if (record(error) && record(error.error)) {
    const p = error.error;
    if (
      typeof p.code === "string" &&
      /^[A-Z][A-Z0-9_]{2,63}$/u.test(p.code) &&
      typeof p.message === "string" &&
      p.message.length > 0 &&
      p.message.length <= 512 &&
      !p.message.includes("\0") &&
      typeof p.correlationId === "string" &&
      p.correlationId.length > 0 &&
      p.correlationId.length <= 128 &&
      typeof p.retryable === "boolean"
    )
      return {
        ok: false,
        error: {
          code: p.code,
          message: p.message,
          correlationId: p.correlationId,
          retryable: p.retryable,
          status,
          ...(unknown ? { outcomeUnknown: true } : {}),
        },
      };
  }
  return fail(
    "WORKBENCH_SAVED_RESOURCE_UNAVAILABLE",
    mutation
      ? "The saved-resource response could not be verified."
      : "Saved resources could not be loaded.",
    true,
    mutation,
  );
}
export async function listSavedResources(
  client: DataGroundClient,
  scope: string,
  cursor?: string,
): Promise<Result<{ page: { items: SavedResource[]; nextCursor?: string } }>> {
  if (!scopePattern.test(scope) || (cursor !== undefined && !cursorPattern.test(cursor)))
    return fail(
      "WORKBENCH_INVALID_SAVED_RESOURCE_REQUEST",
      "The saved-resource scope or cursor is invalid.",
    );
  try {
    const { data, error, response } = await client.GET(root, {
      params: {
        path: { isolationDomainId: scope },
        query: { limit: 50, ...(cursor ? { cursor } : {}) },
      },
    });
    if (!data) return responseFailure(error, response.status);
    const invalid = () =>
      fail(
        "WORKBENCH_SAVED_RESOURCE_SCOPE_MISMATCH",
        "Saved resources did not match the requested scope or contract.",
      );
    const value: unknown = data;
    if (
      response.status !== 200 ||
      !record(value) ||
      !Array.isArray(value.items) ||
      value.items.length > 50
    )
      return invalid();
    const items: SavedResource[] = [];
    for (const raw of value.items) {
      if (
        !record(raw) ||
        raw.isolationDomainId !== scope ||
        !validSavedReference(raw.kind, raw.resourceId) ||
        !stamp(raw.savedAt)
      )
        return invalid();
      const id = raw.resourceId as string;
      const resource =
        raw.resource === undefined ? undefined : decodeResourceSummary(raw.resource, scope);
      if (
        raw.resource !== undefined &&
        (!resource || resource.kind !== raw.kind || resource.metadata.id !== id)
      )
        return invalid();
      const previous = items.at(-1);
      const order = previous ? compareTimestamps(previous.savedAt, raw.savedAt) : 0;
      if (
        items.some((item) => item.resourceId === id) ||
        (previous && (order < 0 || (order === 0 && previous.resourceId <= id)))
      )
        return invalid();
      items.push({
        metadata: { id },
        isolationDomainId: scope,
        kind: raw.kind,
        resourceId: id,
        savedAt: raw.savedAt,
        ...(resource ? { resource } : {}),
      } as SavedResource);
    }
    if (
      value.nextCursor !== undefined &&
      (typeof value.nextCursor !== "string" ||
        !cursorPattern.test(value.nextCursor) ||
        value.nextCursor === cursor ||
        items.length === 0)
    )
      return invalid();
    return {
      ok: true,
      page: { items, ...(value.nextCursor === undefined ? {} : { nextCursor: value.nextCursor }) },
    };
  } catch {
    return fail(
      "WORKBENCH_SAVED_RESOURCE_LIST_UNAVAILABLE",
      "Saved resources could not be loaded. Refresh to try again.",
      true,
    );
  }
}
export async function setSavedResource(
  client: DataGroundClient,
  scope: string,
  request: SavedResourceRequest,
  key: string,
): Promise<Result<{ state: components["schemas"]["SavedResourceState"] }>> {
  if (
    !scopePattern.test(scope) ||
    !validSavedReference(request.kind, request.resourceId) ||
    typeof request.saved !== "boolean" ||
    !/^[A-Za-z0-9._:-]{8,128}$/u.test(key)
  )
    return fail(
      "WORKBENCH_INVALID_SAVED_RESOURCE_REQUEST",
      "The saved-resource request is invalid.",
    );
  try {
    const { data, error, response } = await client.POST(root, {
      params: { path: { isolationDomainId: scope }, header: { "Idempotency-Key": key } },
      body: { kind: request.kind, resourceId: request.resourceId, saved: request.saved },
    });
    if (!data) return responseFailure(error, response.status, true);
    const value: unknown = data;
    if (
      response.status !== 200 ||
      !record(value) ||
      value.isolationDomainId !== scope ||
      value.kind !== request.kind ||
      value.resourceId !== request.resourceId ||
      value.saved !== request.saved ||
      !stamp(value.updatedAt)
    )
      return fail(
        "WORKBENCH_SAVED_RESOURCE_RECEIPT_MISMATCH",
        "The saved-resource receipt could not be verified. Retry the same request to recover.",
        true,
        true,
      );
    return {
      ok: true,
      state: {
        isolationDomainId: scope,
        kind: request.kind,
        resourceId: request.resourceId,
        saved: request.saved,
        updatedAt: value.updatedAt,
      },
    };
  } catch {
    return fail(
      "WORKBENCH_SAVED_RESOURCE_OUTCOME_UNKNOWN",
      "The saved-resource outcome is unknown. Retry the same request to recover.",
      true,
      true,
    );
  }
}
