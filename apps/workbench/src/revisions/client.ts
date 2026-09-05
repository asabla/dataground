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

export interface ServiceRevisionHistoryResource {
  inputSchema?: Record<string, unknown>;
  metadata: ServiceRevisionMetadata;
  outputSchema?: Record<string, unknown>;
  publishedAt?: string;
  requiredCapabilities: string[];
  revisionNumber: number;
  runtimeProfile: string;
  serviceId: string;
  state: "draft" | "published" | "retired";
}

export interface ServiceRevisionPage {
  items: ServiceRevisionHistoryResource[];
  nextCursor?: string;
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

export type ServiceRevisionListResult =
  | { ok: true; page: ServiceRevisionPage }
  | { error: ServiceRevisionFailure; ok: false };

export type ServiceRevisionRetireResult =
  | { ok: true; revision: ServiceRevisionHistoryResource }
  | { ok: false; error: ServiceRevisionFailure };

export async function retireServiceRevision(
  client: DataGroundClient,
  revision: ServiceRevisionHistoryResource,
  idempotencyKey: string,
): Promise<ServiceRevisionRetireResult> {
  const expected = decodeRevisionHistory(
    revision,
    revision?.metadata?.isolationDomainId,
    revision?.serviceId,
  );
  if (
    expected?.state !== "published" ||
    !patterns.isolationDomainId.test(expected.metadata.isolationDomainId) ||
    !patterns.serviceId.test(expected.serviceId) ||
    !patterns.idempotencyKey.test(idempotencyKey)
  ) {
    return {
      ok: false,
      error: {
        code: "WORKBENCH_INVALID_RETIREMENT_REQUEST",
        message: "A published revision and stable retirement request are required.",
        retryable: false,
      },
    };
  }
  try {
    const { data, error, response } = await client.POST(
      "/v1/isolation-domains/{isolationDomainId}/service-revisions/{revisionId}/actions/retire",
      {
        params: {
          path: {
            isolationDomainId: expected.metadata.isolationDomainId,
            revisionId: expected.metadata.id,
          },
          header: { "Idempotency-Key": idempotencyKey },
        },
        body: { expectedVersion: expected.metadata.version },
      },
    );
    if (!data) {
      const failed = failedResult(error, response.status);
      return {
        ok: false,
        error: {
          ...failed.error,
          ...(response.ok ||
          response.status >= 500 ||
          failed.error.code === "WORKBENCH_INVALID_RESPONSE"
            ? { outcomeUnknown: true, retryable: true }
            : {}),
        },
      };
    }
    const retired = decodeRevisionHistory(
      data,
      expected.metadata.isolationDomainId,
      expected.serviceId,
    );
    if (
      retired?.state !== "retired" ||
      retired.metadata.id !== expected.metadata.id ||
      retired.metadata.version !== expected.metadata.version + 1 ||
      retired.metadata.generation !== expected.metadata.generation + 1 ||
      retired.metadata.createdAt !== expected.metadata.createdAt ||
      retired.metadata.createdBy !== expected.metadata.createdBy ||
      Date.parse(retired.metadata.updatedAt) < Date.parse(expected.metadata.updatedAt) ||
      retired.publishedAt !== expected.publishedAt ||
      retired.revisionNumber !== expected.revisionNumber ||
      retired.runtimeProfile !== expected.runtimeProfile ||
      canonicalJSON(retired.requiredCapabilities) !==
        canonicalJSON(expected.requiredCapabilities) ||
      !schemaMatches(retired.inputSchema, expected.inputSchema) ||
      !schemaMatches(retired.outputSchema, expected.outputSchema)
    ) {
      return {
        ok: false,
        error: {
          code: "WORKBENCH_RETIREMENT_UNCONFIRMED",
          message:
            "DataGround returned retirement state that could not be confirmed. Recover the same request before starting another.",
          outcomeUnknown: true,
          retryable: true,
        },
      };
    }
    return { ok: true, revision: retired };
  } catch {
    return {
      ok: false,
      error: {
        code: "WORKBENCH_RETIREMENT_UNCONFIRMED",
        message:
          "The Workbench could not confirm whether DataGround retired the revision. Recover the same request.",
        outcomeUnknown: true,
        retryable: true,
      },
    };
  }
}

const createPath = "/v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/revisions";
const maximumRequestBytes = 1 << 20;
const patterns = {
  cursor: /^[A-Za-z0-9_-]{1,512}$/u,
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
    Date.parse(metadata.updatedAt) < Date.parse(metadata.createdAt) ||
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

function decodeRevisionHistory(
  value: unknown,
  isolationDomainId: string,
  serviceId: string,
): ServiceRevisionHistoryResource | undefined {
  if (!isRecord(value) || !isRecord(value.metadata)) return undefined;
  const metadata = value.metadata;
  const inputSchema =
    value.inputSchema === undefined ? undefined : normalizeSchema(value.inputSchema);
  const outputSchema =
    value.outputSchema === undefined ? undefined : normalizeSchema(value.outputSchema);
  const state = value.state;
  const publishedAt = value.publishedAt;
  if (
    !boundedString(metadata.id, 36, patterns.revisionId) ||
    metadata.isolationDomainId !== isolationDomainId ||
    !isPositiveInteger(metadata.generation) ||
    !isPositiveInteger(metadata.version) ||
    !isTimestamp(metadata.createdAt) ||
    !isTimestamp(metadata.updatedAt) ||
    Date.parse(metadata.updatedAt) < Date.parse(metadata.createdAt) ||
    !boundedString(metadata.createdBy, 128) ||
    value.serviceId !== serviceId ||
    !isPositiveInteger(value.revisionNumber) ||
    (state !== "draft" && state !== "published" && state !== "retired") ||
    !boundedString(value.runtimeProfile, 128) ||
    value.runtimeProfile !== value.runtimeProfile.trim() ||
    value.runtimeProfile.includes("\0") ||
    !Array.isArray(value.requiredCapabilities) ||
    value.requiredCapabilities.length > 8192 ||
    value.requiredCapabilities.some(
      (capability) =>
        !boundedString(capability, 128) ||
        capability !== capability.trim() ||
        capability.includes("\0"),
    ) ||
    new Set(value.requiredCapabilities).size !== value.requiredCapabilities.length ||
    (value.inputSchema !== undefined && inputSchema === undefined) ||
    (value.outputSchema !== undefined && outputSchema === undefined) ||
    (state === "draft" && publishedAt !== undefined) ||
    (state !== "draft" &&
      (!isTimestamp(publishedAt) ||
        Date.parse(publishedAt) < Date.parse(metadata.createdAt) ||
        Date.parse(publishedAt) > Date.parse(metadata.updatedAt)))
  ) {
    return undefined;
  }
  const normalizedPublishedAt = typeof publishedAt === "string" ? publishedAt : undefined;
  const revision = {
    ...(inputSchema === undefined ? undefined : { inputSchema }),
    metadata: {
      createdAt: metadata.createdAt,
      createdBy: metadata.createdBy,
      generation: metadata.generation,
      id: metadata.id,
      isolationDomainId: metadata.isolationDomainId,
      updatedAt: metadata.updatedAt,
      version: metadata.version,
    },
    ...(outputSchema === undefined ? undefined : { outputSchema }),
    ...(normalizedPublishedAt === undefined ? undefined : { publishedAt: normalizedPublishedAt }),
    requiredCapabilities: [...value.requiredCapabilities],
    revisionNumber: value.revisionNumber,
    runtimeProfile: value.runtimeProfile,
    serviceId,
    state,
  } satisfies ServiceRevisionHistoryResource;
  const serialized = canonicalJSON(revision);
  return serialized !== undefined &&
    new TextEncoder().encode(serialized).byteLength <= maximumRequestBytes
    ? revision
    : undefined;
}

function decodeRevisionPage(
  value: unknown,
  isolationDomainId: string,
  serviceId: string,
): ServiceRevisionPage | undefined {
  if (!isRecord(value) || !Array.isArray(value.items) || value.items.length > 100) return undefined;
  const items: ServiceRevisionHistoryResource[] = [];
  const revisionIds = new Set<string>();
  const revisionNumbers = new Set<number>();
  for (const candidate of value.items) {
    const revision = decodeRevisionHistory(candidate, isolationDomainId, serviceId);
    if (
      !revision ||
      revisionIds.has(revision.metadata.id) ||
      revisionNumbers.has(revision.revisionNumber)
    ) {
      return undefined;
    }
    const previous = items.at(-1);
    if (
      previous &&
      (previous.revisionNumber < revision.revisionNumber ||
        (previous.revisionNumber === revision.revisionNumber &&
          previous.metadata.id <= revision.metadata.id))
    ) {
      return undefined;
    }
    revisionIds.add(revision.metadata.id);
    revisionNumbers.add(revision.revisionNumber);
    items.push(revision);
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

export async function listServiceRevisions(
  client: DataGroundClient,
  isolationDomainId: string,
  serviceId: string,
  cursor?: string,
): Promise<ServiceRevisionListResult> {
  if (
    !patterns.isolationDomainId.test(isolationDomainId) ||
    !patterns.serviceId.test(serviceId) ||
    (cursor !== undefined && !patterns.cursor.test(cursor))
  ) {
    return {
      error: {
        code: "WORKBENCH_INVALID_REVISION_LIST_REQUEST",
        message: "The revision-list scope or cursor is invalid.",
        retryable: false,
      },
      ok: false,
    };
  }
  try {
    const { data, error, response } = await client.GET(createPath, {
      params: {
        path: { isolationDomainId, serviceId },
        query: { ...(cursor === undefined ? undefined : { cursor }), limit: 50 },
      },
    });
    if (!data) return failedResult(error, response.status);
    const page = decodeRevisionPage(data, isolationDomainId, serviceId);
    if (page?.nextCursor !== undefined && page.nextCursor === cursor) {
      return {
        error: {
          code: "WORKBENCH_REVISION_LIST_CURSOR_STALLED",
          message: "DataGround returned a revision-list cursor that did not advance.",
          retryable: false,
        },
        ok: false,
      };
    }
    return page
      ? { ok: true, page }
      : {
          error: {
            code: "WORKBENCH_REVISION_LIST_SCOPE_MISMATCH",
            message:
              "DataGround returned revision history outside the requested scope or contract.",
            retryable: false,
          },
          ok: false,
        };
  } catch {
    return {
      error: {
        code: "WORKBENCH_REVISION_LIST_UNAVAILABLE",
        message: "The Workbench could not reach DataGround to list service revisions.",
        retryable: true,
      },
      ok: false,
    };
  }
}

export interface ServiceRevisionReadScope {
  isolationDomainId: string;
  serviceId: string;
  revisionId: string;
}

export type ServiceRevisionReadResult =
  | { ok: true; revision: ServiceRevisionHistoryResource }
  | { ok: false; error: ServiceRevisionFailure };

export async function readServiceRevision(
  client: DataGroundClient,
  scope: ServiceRevisionReadScope,
): Promise<ServiceRevisionReadResult> {
  if (
    !patterns.isolationDomainId.test(scope.isolationDomainId) ||
    !patterns.serviceId.test(scope.serviceId) ||
    !patterns.revisionId.test(scope.revisionId)
  ) {
    return {
      ok: false,
      error: {
        code: "WORKBENCH_INVALID_REVISION_READ_REQUEST",
        message: "The revision scope is invalid.",
        retryable: false,
      },
    };
  }
  try {
    const { data, error, response } = await client.GET(
      "/v1/isolation-domains/{isolationDomainId}/service-revisions/{revisionId}",
      {
        params: {
          path: { isolationDomainId: scope.isolationDomainId, revisionId: scope.revisionId },
        },
      },
    );
    if (response.status !== 200) return failedResult(error, response.status);
    const revision = decodeRevisionHistory(data, scope.isolationDomainId, scope.serviceId);
    if (!revision || revision.metadata.id !== scope.revisionId)
      return {
        ok: false,
        error: {
          code: "WORKBENCH_REVISION_READ_SCOPE_MISMATCH",
          message: "DataGround returned a revision outside the requested scope or contract.",
          retryable: false,
        },
      };
    return { ok: true, revision };
  } catch {
    return {
      ok: false,
      error: {
        code: "WORKBENCH_REVISION_READ_UNAVAILABLE",
        message: "The Workbench could not reach DataGround to read the service revision.",
        retryable: true,
      },
    };
  }
}
