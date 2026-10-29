import type { DataGroundClient } from "../contracts/client";
import type { components } from "../contracts/openapi.gen";
import { scopeRunStates } from "../invocations/client";
import type { AgentServiceFailure } from "../services/client";

export type SearchResult = components["schemas"]["ResourceSearchResult"];
export type SearchKind = SearchResult["kind"];
export const searchKinds: SearchKind[] = ["workspace", "agent-service", "invocation", "artifact"];
export const searchKindLabels: Record<SearchKind, string> = {
  workspace: "Workspace",
  "agent-service": "Agent service",
  invocation: "Run",
  artifact: "Artifact",
};
const patterns = {
  workspace: /^wsp_[0-9a-z]{20,32}$/u,
  "agent-service": /^svc_[0-9a-z]{20,32}$/u,
  invocation: /^inv_[0-9a-z]{20,32}$/u,
  artifact: /^art_[0-9a-z]{20,32}$/u,
};
const scopePattern = /^iso_[0-9a-z]{20,32}$/u;
const cursorPattern = /^[A-Za-z0-9_-]{1,512}$/u;
const record = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" && v !== null && !Array.isArray(v);
const text = (v: unknown, max: number): v is string =>
  typeof v === "string" && Array.from(v).length <= max && !v.includes("\0");
const stamp = (v: unknown): v is string =>
  typeof v === "string" &&
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/u.test(v) &&
  Number.isFinite(Date.parse(v));
const fold = (v: string) => v.replace(/[A-Z]/gu, (c) => c.toLowerCase());
export function validSearchText(value: string) {
  return value.length > 0 && Array.from(value).length <= 128 && !/[\p{Cc}\p{Cs}]/u.test(value);
}

// PostgreSQL and Go retain fractions smaller than a millisecond. Keep those
// fractions when validating page order rather than rounding them into an ID tie.
export function compareSearchCreatedAt(left: string, right: string): number {
  const milliseconds = Date.parse(left) - Date.parse(right);
  if (milliseconds) return milliseconds;
  const fraction = (v: string) => (/\.(\d+)/u.exec(v)?.[1] ?? "").padEnd(9, "0").slice(3);
  return fraction(left).localeCompare(fraction(right));
}
function decode(value: unknown, scope: string): SearchResult | undefined {
  if (!record(value) || !record(value.metadata) || !searchKinds.includes(value.kind as SearchKind))
    return;
  const kind = value.kind as SearchKind,
    m = value.metadata;
  if (
    typeof m.id !== "string" ||
    !patterns[kind].test(m.id) ||
    m.isolationDomainId !== scope ||
    !stamp(m.createdAt) ||
    !text(value.name, 512) ||
    !value.name.trim()
  )
    return;
  const metadata = { id: m.id, isolationDomainId: scope, createdAt: m.createdAt };
  if (
    value.workspaceId !== undefined &&
    (typeof value.workspaceId !== "string" || !patterns.workspace.test(value.workspaceId))
  )
    return;
  const workspace =
    value.workspaceId === undefined ? {} : { workspaceId: value.workspaceId as string };
  if (kind === "workspace" || kind === "agent-service") {
    if (value.description !== undefined && !text(value.description, 4096)) return;
    return {
      metadata,
      kind,
      name: value.name,
      ...(kind === "agent-service" ? workspace : {}),
      ...(value.description === undefined ? {} : { description: value.description as string }),
    };
  }
  if (typeof value.serviceId !== "string" || !patterns["agent-service"].test(value.serviceId))
    return;
  if (kind === "invocation") {
    if (!scopeRunStates.includes(value.state as (typeof scopeRunStates)[number])) return;
    return {
      metadata,
      kind,
      name: value.name,
      ...workspace,
      serviceId: value.serviceId,
      state: value.state as (typeof scopeRunStates)[number],
    };
  }
  if (
    typeof value.invocationId !== "string" ||
    !patterns.invocation.test(value.invocationId) ||
    !["pending", "available", "failed", "deleted"].includes(value.state as string)
  )
    return;
  return {
    metadata,
    kind,
    name: value.name,
    ...workspace,
    serviceId: value.serviceId,
    invocationId: value.invocationId,
    state: value.state as "pending" | "available" | "failed" | "deleted",
  };
}
function failure(
  code: string,
  message: string,
  retryable = false,
): { ok: false; error: AgentServiceFailure } {
  return { ok: false, error: { code, message, retryable } };
}
export async function searchResources(
  client: DataGroundClient,
  scope: string,
  query: string,
  kind?: SearchKind,
  cursor?: string,
): Promise<
  | { ok: true; page: { items: SearchResult[]; nextCursor?: string } }
  | { ok: false; error: AgentServiceFailure }
> {
  if (
    !scopePattern.test(scope) ||
    !validSearchText(query) ||
    (kind !== undefined && !searchKinds.includes(kind)) ||
    (cursor !== undefined && !cursorPattern.test(cursor))
  )
    return failure(
      "WORKBENCH_INVALID_SEARCH",
      "The search text, resource kind, scope or cursor is invalid.",
    );
  try {
    const { data, error, response } = await client.GET(
      "/v1/isolation-domains/{isolationDomainId}/search",
      {
        params: {
          path: { isolationDomainId: scope },
          query: { q: query, limit: 50, ...(kind ? { kind } : {}), ...(cursor ? { cursor } : {}) },
        },
      },
    );
    if (!data) {
      if (record(error) && record(error.error)) {
        const p = error.error;
        if (
          text(p.code, 64) &&
          /^[A-Z][A-Z0-9_]{2,63}$/u.test(p.code) &&
          text(p.message, 512) &&
          p.message.length > 0 &&
          text(p.correlationId, 128) &&
          p.correlationId.length > 0 &&
          typeof p.retryable === "boolean"
        )
          return {
            ok: false,
            error: {
              code: p.code,
              message: p.message,
              retryable: p.retryable,
              correlationId: p.correlationId,
              status: response.status,
            },
          };
      }
      return failure("WORKBENCH_INVALID_RESPONSE", "The search response could not be verified.");
    }
    const value: unknown = data;
    const invalid = () =>
      failure(
        "WORKBENCH_SEARCH_SCOPE_MISMATCH",
        "Search results did not match the requested scope, filters or contract.",
      );
    if (
      response.status !== 200 ||
      !record(value) ||
      !Array.isArray(value.items) ||
      value.items.length > 50
    )
      return invalid();
    const items: SearchResult[] = [];
    for (const raw of value.items) {
      const item = decode(raw, scope),
        previous = items.at(-1);
      if (
        !item ||
        (kind && item.kind !== kind) ||
        ![
          item.metadata.id,
          item.name,
          ...("description" in item && item.description ? [item.description] : []),
        ].some((v) => fold(v).includes(fold(query))) ||
        items.some((v) => v.metadata.id === item.metadata.id) ||
        (previous &&
          (compareSearchCreatedAt(previous.metadata.createdAt, item.metadata.createdAt) < 0 ||
            (compareSearchCreatedAt(previous.metadata.createdAt, item.metadata.createdAt) === 0 &&
              previous.metadata.id <= item.metadata.id)))
      )
        return invalid();
      items.push(item);
    }
    if (
      value.nextCursor !== undefined &&
      (typeof value.nextCursor !== "string" ||
        !cursorPattern.test(value.nextCursor) ||
        value.nextCursor === cursor ||
        items.length === 0)
    )
      return failure(
        "WORKBENCH_SEARCH_CURSOR_INVALID",
        "Search did not advance. Refresh to recover.",
      );
    return {
      ok: true,
      page: { items, ...(value.nextCursor === undefined ? {} : { nextCursor: value.nextCursor }) },
    };
  } catch {
    return failure(
      "WORKBENCH_SEARCH_UNAVAILABLE",
      "Resources could not be searched. Try again.",
      true,
    );
  }
}
