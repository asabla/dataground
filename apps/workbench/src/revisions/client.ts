import type { DataGroundClient } from "../contracts/client";
import type { components } from "../contracts/openapi.gen";

type ErrorEnvelope = components["schemas"]["ErrorEnvelope"];

export interface ServiceRevisionMetadata {
  createdAt: string;
  createdBy: string;
  generation: number;
  id: string;
  isolationDomainId: string;
  updatedAt: string;
  version: number;
}

export interface ServiceRevisionResource {
  inputSchema?: Record<string, unknown>;
  metadata: ServiceRevisionMetadata;
  outputSchema?: Record<string, unknown>;
  requiredCapabilities: string[];
  revisionNumber: number;
  runtimeProfile: string;
  serviceId: string;
  state: "draft";
}

export interface ServiceRevisionCreateRequest {
  inputSchema?: Record<string, unknown>;
  outputSchema?: Record<string, unknown>;
  requiredCapabilities: string[];
  runtimeProfile: string;
}

export interface ServiceRevisionFailure {
  code: string;
  correlationId?: string;
  message: string;
  outcomeUnknown?: boolean;
  retryable: boolean;
  status?: number;
}

export type ServiceRevisionCreateResult =
  | { ok: true; revision: ServiceRevisionResource }
  | { error: ServiceRevisionFailure; ok: false };

const createPath = "/v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/revisions";
const maximumRequestBytes = 1 << 20;
const patterns = {
  errorCode: /^[A-Z][A-Z0-9_]{2,63}$/u,
  idempotencyKey: /^[A-Za-z0-9._:-]{8,128}$/u,
  isolationDomainId: /^iso_[0-9a-z]{20,32}$/u,
  revisionId: /^rev_[0-9a-z]{20,32}$/u,
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

function canonicalJSON(value: unknown): string | undefined {
  const visit = (candidate: unknown): unknown => {
    if (Array.isArray(candidate)) return candidate.map(visit);
    if (!isRecord(candidate)) return candidate;
    return Object.fromEntries(
      Object.keys(candidate)
        .sort()
        .map((key) => [key, visit(candidate[key])]),
    );
  };
  try {
    return JSON.stringify(visit(value));
  } catch {
    return undefined;
  }
}

function normalizeSchema(value: unknown): Record<string, unknown> | undefined {
  if (!isRecord(value)) return undefined;
  try {
    const serialized = JSON.stringify(value);
    const parsed: unknown = JSON.parse(serialized);
    return isRecord(parsed) ? parsed : undefined;
  } catch {
    return undefined;
  }
}

function normalizeRequest(
  request: ServiceRevisionCreateRequest,
): ServiceRevisionCreateRequest | undefined {
  if (typeof request.runtimeProfile !== "string" || !Array.isArray(request.requiredCapabilities)) {
    return undefined;
  }
  const runtimeProfile = request.runtimeProfile.trim();
  if (request.requiredCapabilities.some((capability) => typeof capability !== "string")) {
    return undefined;
  }
  const requiredCapabilities = request.requiredCapabilities.map((capability) => capability.trim());
  const encoder = new TextEncoder();
  if (
    !boundedString(runtimeProfile, 128) ||
    requiredCapabilities.some(
      (capability) => !boundedString(capability, 128) || capability.includes("\0"),
    ) ||
    new Set(requiredCapabilities).size !== requiredCapabilities.length
  ) {
    return undefined;
  }
  const inputSchema =
    request.inputSchema === undefined ? undefined : normalizeSchema(request.inputSchema);
  const outputSchema =
    request.outputSchema === undefined ? undefined : normalizeSchema(request.outputSchema);
  if (
    (request.inputSchema !== undefined && inputSchema === undefined) ||
    (request.outputSchema !== undefined && outputSchema === undefined)
  ) {
    return undefined;
  }
  const normalized = {
    ...(inputSchema === undefined ? undefined : { inputSchema }),
    ...(outputSchema === undefined ? undefined : { outputSchema }),
    requiredCapabilities,
    runtimeProfile,
  };
  const serialized = canonicalJSON(normalized);
  return serialized !== undefined && encoder.encode(serialized).byteLength <= maximumRequestBytes
    ? normalized
    : undefined;
}

function schemaMatches(actual: unknown, expected: Record<string, unknown> | undefined): boolean {
  if (expected === undefined) return actual === undefined;
  return isRecord(actual) && canonicalJSON(actual) === canonicalJSON(expected);
}

function decodeRevision(
  value: unknown,
  isolationDomainId: string,
  serviceId: string,
  expected: ServiceRevisionCreateRequest,
): ServiceRevisionResource | undefined {
  if (!isRecord(value) || !isRecord(value.metadata)) return undefined;
  const metadata = value.metadata;
  if (
    !boundedString(metadata.id, 36, patterns.revisionId) ||
    metadata.isolationDomainId !== isolationDomainId ||
    !isPositiveInteger(metadata.generation) ||
    !isPositiveInteger(metadata.version) ||
    !isTimestamp(metadata.createdAt) ||
    !isTimestamp(metadata.updatedAt) ||
    !boundedString(metadata.createdBy, 128) ||
    value.serviceId !== serviceId ||
    !isPositiveInteger(value.revisionNumber) ||
    value.state !== "draft" ||
    value.runtimeProfile !== expected.runtimeProfile ||
    !Array.isArray(value.requiredCapabilities) ||
    value.requiredCapabilities.length !== expected.requiredCapabilities.length ||
    value.requiredCapabilities.some(
      (capability, index) => capability !== expected.requiredCapabilities[index],
    ) ||
    !schemaMatches(value.inputSchema, expected.inputSchema) ||
    !schemaMatches(value.outputSchema, expected.outputSchema) ||
    value.publishedAt !== undefined
  ) {
    return undefined;
  }
  return {
    ...(expected.inputSchema === undefined ? undefined : { inputSchema: expected.inputSchema }),
    metadata: {
      createdAt: metadata.createdAt,
      createdBy: metadata.createdBy,
      generation: metadata.generation,
      id: metadata.id,
      isolationDomainId: metadata.isolationDomainId,
      updatedAt: metadata.updatedAt,
      version: metadata.version,
    },
    ...(expected.outputSchema === undefined ? undefined : { outputSchema: expected.outputSchema }),
    requiredCapabilities: [...expected.requiredCapabilities],
    revisionNumber: value.revisionNumber,
    runtimeProfile: expected.runtimeProfile,
    serviceId,
    state: "draft",
  };
}

function failedResult(
  error: ErrorEnvelope | undefined,
  status: number,
): Extract<ServiceRevisionCreateResult, { ok: false }> {
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
      message: "DataGround returned a revision response the Workbench could not interpret.",
      retryable: false,
      status,
    },
    ok: false,
  };
}

function invalidRequest(): ServiceRevisionCreateResult {
  return {
    error: {
      code: "WORKBENCH_INVALID_REVISION_REQUEST",
      message: "The revision scope, definition, or request identifier is invalid.",
      retryable: false,
    },
    ok: false,
  };
}

export async function createServiceRevision(
  client: DataGroundClient,
  isolationDomainId: string,
  serviceId: string,
  request: ServiceRevisionCreateRequest,
  idempotencyKey: string,
): Promise<ServiceRevisionCreateResult> {
  const body = normalizeRequest(request);
  if (
    !patterns.isolationDomainId.test(isolationDomainId) ||
    !patterns.serviceId.test(serviceId) ||
    !patterns.idempotencyKey.test(idempotencyKey) ||
    body === undefined
  ) {
    return invalidRequest();
  }
  try {
    const { data, error, response } = await client.POST(createPath, {
      body,
      params: {
        header: { "Idempotency-Key": idempotencyKey },
        path: { isolationDomainId, serviceId },
      },
    });
    if (!data) return failedResult(error, response.status);
    const revision = decodeRevision(data, isolationDomainId, serviceId, body);
    return revision
      ? { ok: true, revision }
      : {
          error: {
            code: "WORKBENCH_REVISION_SCOPE_MISMATCH",
            message: "DataGround returned revision state outside the requested scope or contract.",
            retryable: false,
          },
          ok: false,
        };
  } catch {
    return {
      error: {
        code: "WORKBENCH_REVISION_CREATION_UNCONFIRMED",
        message: "The Workbench could not confirm whether DataGround created the revision draft.",
        outcomeUnknown: true,
        retryable: true,
      },
      ok: false,
    };
  }
}
