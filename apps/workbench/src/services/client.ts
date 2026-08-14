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

export type AgentServiceCreateResult =
  | { ok: true; service: AgentServiceResource }
  | { error: AgentServiceFailure; ok: false };

const createPath = "/v1/isolation-domains/{isolationDomainId}/agent-services";
const patterns = {
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
  expected: AgentServiceCreateRequest,
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
    !boundedString(metadata.createdBy, 128) ||
    !boundedString(value.name, 128) ||
    value.name !== expected.name ||
    (value.description !== undefined &&
      (typeof value.description !== "string" ||
        new TextEncoder().encode(value.description).byteLength > 2048)) ||
    value.description !== expected.description
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
