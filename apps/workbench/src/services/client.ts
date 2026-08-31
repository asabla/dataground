import type { DataGroundClient } from "../contracts/client";
import type { components } from "../contracts/openapi.gen";

type ErrorEnvelope = components["schemas"]["ErrorEnvelope"];

export interface AgentServiceMetadata {
  createdAt: string;
  createdBy: string;
  generation: number;
  id: string;
  isolationDomainId: string;
  updatedAt: string;
  version: number;
}

export interface AgentServiceResource {
  description?: string;
  metadata: AgentServiceMetadata;
  name: string;
}

export interface AgentServiceCreateRequest {
  description?: string;
  name: string;
}

export interface AgentServiceFailure {
  code: string;
  correlationId?: string;
  message: string;
  outcomeUnknown?: boolean;
  retryable: boolean;
  status?: number;
}

export interface AgentServicePage {
  items: AgentServiceResource[];
  nextCursor?: string;
}

export type AgentServiceCreateResult =
  | { ok: true; service: AgentServiceResource }
  | { error: AgentServiceFailure; ok: false };

export type AgentServiceListResult =
  | { ok: true; page: AgentServicePage }
  | { error: AgentServiceFailure; ok: false };

const createPath = "/v1/isolation-domains/{isolationDomainId}/agent-services";
const patterns = {
  cursor: /^[A-Za-z0-9_-]{1,512}$/u,
  errorCode: /^[A-Z][A-Z0-9_]{2,63}$/u,
  idempotencyKey: /^[A-Za-z0-9._:-]{8,128}$/u,
  isolationDomainId: /^iso_[0-9a-z]{20,32}$/u,
  serviceId: /^svc_[0-9a-z]{20,32}$/u,
  timestamp: /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/u,
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function boundedString(value: unknown, maximum: number, pattern?: RegExp): value is string {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    new TextEncoder().encode(value).byteLength <= maximum &&
    (pattern === undefined || pattern.test(value))
  );
}

function isPositiveInteger(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) >= 1;
}

function isTimestamp(value: unknown): value is string {
  return (
    typeof value === "string" && patterns.timestamp.test(value) && !Number.isNaN(Date.parse(value))
  );
}

function decodeService(
  value: unknown,
  isolationDomainId: string,
  expected?: AgentServiceCreateRequest,
): AgentServiceResource | undefined {
  if (!isRecord(value) || !isRecord(value.metadata)) return undefined;
  const metadata = value.metadata;
  if (
    !boundedString(metadata.id, 36, patterns.serviceId) ||
    metadata.isolationDomainId !== isolationDomainId ||
    !isPositiveInteger(metadata.generation) ||
    !isPositiveInteger(metadata.version) ||
    !isTimestamp(metadata.createdAt) ||
    !isTimestamp(metadata.updatedAt) ||
    Date.parse(metadata.updatedAt) < Date.parse(metadata.createdAt) ||
    !boundedString(metadata.createdBy, 128) ||
    !boundedString(value.name, 128) ||
    (expected !== undefined && value.name !== expected.name) ||
    (value.description !== undefined &&
      (typeof value.description !== "string" ||
        new TextEncoder().encode(value.description).byteLength > 2048)) ||
    (expected !== undefined && value.description !== expected.description)
  ) {
    return undefined;
  }
  return {
    ...(value.description === undefined ? undefined : { description: value.description }),
    metadata: {
      createdAt: metadata.createdAt,
      createdBy: metadata.createdBy,
      generation: metadata.generation,
      id: metadata.id,
      isolationDomainId: metadata.isolationDomainId,
      updatedAt: metadata.updatedAt,
      version: metadata.version,
    },
    name: value.name,
  };
}

function decodeServicePage(
  value: unknown,
  isolationDomainId: string,
): AgentServicePage | undefined {
  if (!isRecord(value) || !Array.isArray(value.items) || value.items.length > 100) return undefined;
  const items: AgentServiceResource[] = [];
  const serviceIds = new Set<string>();
  for (const candidate of value.items) {
    const service = decodeService(candidate, isolationDomainId);
    if (!service || serviceIds.has(service.metadata.id)) return undefined;
    const previous = items.at(-1);
    const previousCreatedAt = previous ? Date.parse(previous.metadata.createdAt) : undefined;
    const serviceCreatedAt = Date.parse(service.metadata.createdAt);
    if (
      previous &&
      previousCreatedAt !== undefined &&
      (previousCreatedAt < serviceCreatedAt ||
        (previousCreatedAt === serviceCreatedAt && previous.metadata.id <= service.metadata.id))
    ) {
      return undefined;
    }
    serviceIds.add(service.metadata.id);
    items.push(service);
  }
  if (
    value.nextCursor !== undefined &&
    (typeof value.nextCursor !== "string" || !patterns.cursor.test(value.nextCursor))
  ) {
    return undefined;
  }
  if (value.nextCursor !== undefined && items.length === 0) return undefined;
  return {
    items,
    ...(value.nextCursor === undefined ? undefined : { nextCursor: value.nextCursor }),
  };
}

function failedResult(
  error: ErrorEnvelope | undefined,
  status: number,
): Extract<AgentServiceCreateResult, { ok: false }> {
  const problem = error?.error;
  if (
    problem &&
    boundedString(problem.code, 64, patterns.errorCode) &&
    boundedString(problem.message, 512) &&
    boundedString(problem.correlationId, 128) &&
    typeof problem.retryable === "boolean"
  ) {
    return {
      error: {
        code: problem.code,
        correlationId: problem.correlationId,
        message: problem.message,
        retryable: problem.retryable,
        status,
      },
      ok: false,
    };
  }
  return {
    error: {
      code: "WORKBENCH_INVALID_RESPONSE",
      message: "DataGround returned a service response the Workbench could not interpret.",
      retryable: false,
      status,
    },
    ok: false,
  };
}

function invalidRequest(): AgentServiceCreateResult {
  return {
    error: {
      code: "WORKBENCH_INVALID_SERVICE_REQUEST",
      message: "The isolation domain, service fields, or request identifier is invalid.",
      retryable: false,
    },
    ok: false,
  };
}

export async function createAgentService(
  client: DataGroundClient,
  isolationDomainId: string,
  request: AgentServiceCreateRequest,
  idempotencyKey: string,
): Promise<AgentServiceCreateResult> {
  const name = request.name.trim();
  const encoder = new TextEncoder();
  if (
    !patterns.isolationDomainId.test(isolationDomainId) ||
    name.length === 0 ||
    encoder.encode(name).byteLength > 128 ||
    (request.description !== undefined && encoder.encode(request.description).byteLength > 2048) ||
    !patterns.idempotencyKey.test(idempotencyKey)
  ) {
    return invalidRequest();
  }
  const body = {
    ...(request.description === undefined ? undefined : { description: request.description }),
    name,
  };
  try {
    const { data, error, response } = await client.POST(createPath, {
      body,
      params: {
        header: { "Idempotency-Key": idempotencyKey },
        path: { isolationDomainId },
      },
    });
    if (!data) return failedResult(error, response.status);
    const service = decodeService(data, isolationDomainId, body);
    return service
      ? { ok: true, service }
      : {
          error: {
            code: "WORKBENCH_SERVICE_SCOPE_MISMATCH",
            message: "DataGround returned service state outside the requested scope or contract.",
            retryable: false,
          },
          ok: false,
        };
  } catch {
    return {
      error: {
        code: "WORKBENCH_SERVICE_CREATION_UNCONFIRMED",
        message: "The Workbench could not confirm whether DataGround created the service.",
        outcomeUnknown: true,
        retryable: true,
      },
      ok: false,
    };
  }
}

export async function listAgentServices(
  client: DataGroundClient,
  isolationDomainId: string,
  cursor?: string,
): Promise<AgentServiceListResult> {
  if (
    !patterns.isolationDomainId.test(isolationDomainId) ||
    (cursor !== undefined && !patterns.cursor.test(cursor))
  ) {
    return {
      error: {
        code: "WORKBENCH_INVALID_SERVICE_LIST_REQUEST",
        message: "The isolation domain or service-list cursor is invalid.",
        retryable: false,
      },
      ok: false,
    };
  }
  try {
    const { data, error, response } = await client.GET(createPath, {
      params: {
        path: { isolationDomainId },
        query: { ...(cursor === undefined ? undefined : { cursor }), limit: 50 },
      },
    });
    if (!data) return failedResult(error, response.status);
    const page = decodeServicePage(data, isolationDomainId);
    if (page?.nextCursor !== undefined && page.nextCursor === cursor) {
      return {
        error: {
          code: "WORKBENCH_SERVICE_LIST_CURSOR_STALLED",
          message: "DataGround returned a service-list cursor that did not advance.",
          retryable: false,
        },
        ok: false,
      };
    }
    return page
      ? { ok: true, page }
      : {
          error: {
            code: "WORKBENCH_SERVICE_LIST_SCOPE_MISMATCH",
            message: "DataGround returned service state outside the requested scope or contract.",
            retryable: false,
          },
          ok: false,
        };
  } catch {
    return {
      error: {
        code: "WORKBENCH_SERVICE_LIST_UNAVAILABLE",
        message: "The Workbench could not reach DataGround to list agent services.",
        retryable: true,
      },
      ok: false,
    };
  }
}
