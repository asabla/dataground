import type { DataGroundClient } from "../contracts/client";
import type { AgentServiceFailure, AgentServiceMetadata } from "../services/client";

export interface WorkspaceResource {
  metadata: AgentServiceMetadata;
  name: string;
  description?: string;
}
export interface WorkspacePage {
  items: WorkspaceResource[];
  nextCursor?: string;
}
type Result<T> = ({ ok: true } & T) | { ok: false; error: AgentServiceFailure };
const root = "/v1/isolation-domains/{isolationDomainId}/workspaces";
const workspacePattern = /^wsp_[0-9a-z]{20,32}$/u;
const scopePattern = /^iso_[0-9a-z]{20,32}$/u;
const cursorPattern = /^[A-Za-z0-9_-]{1,512}$/u;
const record = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" && v !== null && !Array.isArray(v);
const text = (v: unknown, max: number): v is string =>
  typeof v === "string" && new TextEncoder().encode(v).length <= max && !v.includes("\0");
const stamp = (v: unknown): v is string =>
  typeof v === "string" &&
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/u.test(v) &&
  Number.isFinite(Date.parse(v));
function decode(value: unknown, scope: string): WorkspaceResource | undefined {
  if (!record(value) || !record(value.metadata)) return;
  const m = value.metadata;
  if (
    typeof m.id !== "string" ||
    !workspacePattern.test(m.id) ||
    m.isolationDomainId !== scope ||
    !Number.isSafeInteger(m.generation) ||
    (m.generation as number) < 1 ||
    !Number.isSafeInteger(m.version) ||
    (m.version as number) < 1 ||
    !stamp(m.createdAt) ||
    !stamp(m.updatedAt) ||
    Date.parse(m.updatedAt) < Date.parse(m.createdAt) ||
    !text(m.createdBy, 128) ||
    m.createdBy.length === 0 ||
    !text(value.name, 128) ||
    value.name.trim().length === 0 ||
    (value.description !== undefined && !text(value.description, 2048))
  )
    return;
  return {
    metadata: {
      id: m.id,
      isolationDomainId: scope,
      generation: m.generation as number,
      version: m.version as number,
      createdAt: m.createdAt,
      updatedAt: m.updatedAt,
      createdBy: m.createdBy,
    },
    name: value.name,
    ...(value.description === undefined ? {} : { description: value.description }),
  };
}
function failure(
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
): { ok: false; error: AgentServiceFailure } {
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
          correlationId: p.correlationId,
          retryable: p.retryable,
          status,
        },
      };
  }
  return failure(
    "WORKBENCH_INVALID_RESPONSE",
    "DataGround returned a workspace response that could not be verified.",
  );
}
export async function listWorkspaces(
  client: DataGroundClient,
  scope: string,
  cursor?: string,
): Promise<Result<{ page: WorkspacePage }>> {
  if (!scopePattern.test(scope) || (cursor !== undefined && !cursorPattern.test(cursor)))
    return failure(
      "WORKBENCH_INVALID_WORKSPACE_REQUEST",
      "The workspace scope or cursor is invalid.",
    );
  try {
    const { data, error, response } = await client.GET(root, {
      params: {
        path: { isolationDomainId: scope },
        query: { limit: 50, ...(cursor ? { cursor } : {}) },
      },
    });
    if (!data) return responseFailure(error, response.status);
    const value: unknown = data;
    if (
      response.status !== 200 ||
      !record(value) ||
      !Array.isArray(value.items) ||
      value.items.length > 100
    )
      return failure(
        "WORKBENCH_WORKSPACE_SCOPE_MISMATCH",
        "The workspace list did not match the requested scope or contract.",
      );
    const items: WorkspaceResource[] = [];
    for (const raw of value.items) {
      const item = decode(raw, scope),
        previous = items.at(-1);
      if (
        !item ||
        items.some((v) => v.metadata.id === item.metadata.id) ||
        (previous &&
          (Date.parse(previous.metadata.createdAt) < Date.parse(item.metadata.createdAt) ||
            (Date.parse(previous.metadata.createdAt) === Date.parse(item.metadata.createdAt) &&
              previous.metadata.id <= item.metadata.id)))
      )
        return failure(
          "WORKBENCH_WORKSPACE_SCOPE_MISMATCH",
          "The workspace list did not match the requested scope or contract.",
        );
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
        "WORKBENCH_WORKSPACE_CURSOR_INVALID",
        "The workspace list cursor did not advance or was invalid.",
      );
    return {
      ok: true,
      page: { items, ...(value.nextCursor === undefined ? {} : { nextCursor: value.nextCursor }) },
    };
  } catch {
    return failure(
      "WORKBENCH_WORKSPACE_LIST_UNAVAILABLE",
      "Workspaces could not be loaded. Refresh to try again.",
      true,
    );
  }
}
export async function readWorkspace(
  client: DataGroundClient,
  scope: string,
  id: string,
): Promise<Result<{ workspace: WorkspaceResource }>> {
  if (!scopePattern.test(scope) || !workspacePattern.test(id))
    return failure("WORKBENCH_INVALID_WORKSPACE_REQUEST", "The workspace identity is invalid.");
  try {
    const { data, error, response } = await client.GET(
      "/v1/isolation-domains/{isolationDomainId}/workspaces/{workspaceId}",
      { params: { path: { isolationDomainId: scope, workspaceId: id } } },
    );
    if (!data) return responseFailure(error, response.status);
    const workspace = decode(data, scope);
    return response.status === 200 && workspace?.metadata.id === id
      ? { ok: true, workspace }
      : failure(
          "WORKBENCH_WORKSPACE_SCOPE_MISMATCH",
          "The workspace did not match the requested identity.",
        );
  } catch {
    return failure(
      "WORKBENCH_WORKSPACE_READ_UNAVAILABLE",
      "This workspace could not be loaded. Refresh to try again.",
      true,
    );
  }
}
export async function createWorkspace(
  client: DataGroundClient,
  scope: string,
  request: { name: string; description?: string },
  idempotencyKey: string,
): Promise<Result<{ workspace: WorkspaceResource }>> {
  const name = request.name.trim();
  if (
    !scopePattern.test(scope) ||
    !text(name, 128) ||
    !name ||
    (request.description !== undefined && !text(request.description, 2048)) ||
    !/^[A-Za-z0-9._:-]{8,128}$/u.test(idempotencyKey)
  )
    return failure(
      "WORKBENCH_INVALID_WORKSPACE_REQUEST",
      "The workspace fields or request identifier is invalid.",
    );
  const body = {
    name,
    ...(request.description === undefined ? {} : { description: request.description }),
  };
  try {
    const { data, error, response } = await client.POST(root, {
      body,
      params: { path: { isolationDomainId: scope }, header: { "Idempotency-Key": idempotencyKey } },
    });
    if (!data) {
      const result = responseFailure(error, response.status);
      if (
        result.error.code === "WORKBENCH_INVALID_RESPONSE" &&
        (response.status < 300 || response.status >= 500)
      )
        return { ...result, error: { ...result.error, outcomeUnknown: true, retryable: true } };
      return result;
    }
    const workspace = decode(data, scope);
    return response.status === 201 &&
      workspace?.name === name &&
      workspace.description === body.description
      ? { ok: true, workspace }
      : failure(
          "WORKBENCH_WORKSPACE_CREATION_UNCONFIRMED",
          "The returned workspace could not be verified. Retry the same request to confirm its outcome.",
          true,
          true,
        );
  } catch {
    return failure(
      "WORKBENCH_WORKSPACE_CREATION_UNCONFIRMED",
      "The workspace creation outcome is unknown. Retry the same request to confirm its outcome.",
      true,
      true,
    );
  }
}
