import type { DataGroundClient } from "../contracts/client";
import type { components } from "../contracts/openapi.gen";
import type { PublishedServiceRevisionResource } from "../revisions/publicationClient";

type ErrorEnvelope = components["schemas"]["ErrorEnvelope"];

export interface ServiceAliasMetadata {
  createdAt: string;
  createdBy: string;
  generation: number;
  id: string;
  isolationDomainId: string;
  updatedAt: string;
  version: number;
}

export interface ServiceAliasResource {
  metadata: ServiceAliasMetadata;
  name: string;
  revisionId: string;
  serviceId: string;
}

export interface ServiceAliasReadScope {
  isolationDomainId: string;
  serviceId: string;
}

export interface ServiceAliasFailure {
  code: string;
  correlationId?: string;
  message: string;
  outcomeUnknown?: boolean;
  retryable: boolean;
  status?: number;
}

export type ServiceAliasAssignResult =
  | { alias: ServiceAliasResource; ok: true }
  | { error: ServiceAliasFailure; ok: false };

export type ServiceAliasReadResult =
  | { alias?: ServiceAliasResource; ok: true }
  | { error: ServiceAliasFailure; ok: false };

const assignPath =
  "/v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/aliases/{alias}";
const patterns = {
  aliasId: /^als_[0-9a-z]{20,32}$/u,
  aliasName: /^[a-z](?:[a-z0-9-]*[a-z0-9])?$/u,
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

function isJSONValue(value: unknown, ancestors = new Set<object>()): boolean {
  if (
    value === null ||
    typeof value === "string" ||
    typeof value === "boolean" ||
    (typeof value === "number" && Number.isFinite(value))
  ) {
    return true;
  }
  if (typeof value !== "object") return false;
  if (ancestors.has(value)) return false;
  ancestors.add(value);
  const valid = Array.isArray(value)
    ? value.every((entry) => isJSONValue(entry, ancestors))
    : Object.values(value).every((entry) => isJSONValue(entry, ancestors));
  ancestors.delete(value);
  return valid;
}

function validSchema(value: unknown): boolean {
  return isRecord(value) && isJSONValue(value);
}

function validPublishedRevision(revision: PublishedServiceRevisionResource): boolean {
  const metadata = revision.metadata;
  return (
    patterns.revisionId.test(metadata.id) &&
    patterns.isolationDomainId.test(metadata.isolationDomainId) &&
    patterns.serviceId.test(revision.serviceId) &&
    isPositiveInteger(metadata.generation) &&
    isPositiveInteger(metadata.version) &&
    isPositiveInteger(revision.revisionNumber) &&
    isTimestamp(metadata.createdAt) &&
    isTimestamp(metadata.updatedAt) &&
    Date.parse(metadata.updatedAt) >= Date.parse(metadata.createdAt) &&
    boundedString(metadata.createdBy, 128) &&
    revision.state === "published" &&
    boundedString(revision.runtimeProfile, 128) &&
    revision.runtimeProfile === revision.runtimeProfile.trim() &&
    !revision.runtimeProfile.includes("\0") &&
    Array.isArray(revision.requiredCapabilities) &&
    revision.requiredCapabilities.every(
      (capability) =>
        boundedString(capability, 128) &&
        capability === capability.trim() &&
        !capability.includes("\0"),
    ) &&
    new Set(revision.requiredCapabilities).size === revision.requiredCapabilities.length &&
    (revision.inputSchema === undefined || validSchema(revision.inputSchema)) &&
    (revision.outputSchema === undefined || validSchema(revision.outputSchema)) &&
    isTimestamp(revision.publishedAt) &&
    Date.parse(revision.publishedAt) >= Date.parse(metadata.createdAt) &&
    Date.parse(revision.publishedAt) <= Date.parse(metadata.updatedAt)
  );
}

export function isServiceAliasAssignmentScopeValid(
  revision: PublishedServiceRevisionResource,
  name: string,
  current: ServiceAliasResource | undefined,
): boolean {
  return (
    validPublishedRevision(revision) &&
    boundedString(name, 63, patterns.aliasName) &&
    (current === undefined || validCurrentAlias(current, revision, name))
  );
}

export function isServiceAliasRoutedToRevision(
  alias: ServiceAliasResource,
  revision: PublishedServiceRevisionResource,
): boolean {
  return (
    alias.revisionId === revision.metadata.id &&
    isServiceAliasAssignmentScopeValid(revision, alias.name, alias) &&
    Date.parse(alias.metadata.updatedAt) >= Date.parse(revision.publishedAt)
  );
}

function validCurrentAlias(
  alias: ServiceAliasResource,
  revision: PublishedServiceRevisionResource,
  name: string,
): boolean {
  const metadata = alias.metadata;
  return (
    patterns.aliasId.test(metadata.id) &&
    metadata.isolationDomainId === revision.metadata.isolationDomainId &&
    alias.serviceId === revision.serviceId &&
    alias.name === name &&
    patterns.revisionId.test(alias.revisionId) &&
    isPositiveInteger(metadata.generation) &&
    isPositiveInteger(metadata.version) &&
    isTimestamp(metadata.createdAt) &&
    isTimestamp(metadata.updatedAt) &&
    Date.parse(metadata.updatedAt) >= Date.parse(metadata.createdAt) &&
    boundedString(metadata.createdBy, 128)
  );
}

function decodeAlias(
  value: unknown,
  revision: PublishedServiceRevisionResource,
  name: string,
  current: ServiceAliasResource | undefined,
): ServiceAliasResource | undefined {
  const alias = decodeObservedAlias(
    value,
    {
      isolationDomainId: revision.metadata.isolationDomainId,
      serviceId: revision.serviceId,
    },
    name,
  );
  if (!alias || alias.revisionId !== revision.metadata.id) return undefined;
  const metadata = alias.metadata;
  if (
    current
      ? metadata.id !== current.metadata.id ||
        metadata.createdAt !== current.metadata.createdAt ||
        metadata.createdBy !== current.metadata.createdBy ||
        metadata.generation !== current.metadata.generation + 1 ||
        metadata.version !== current.metadata.version + 1 ||
        Date.parse(metadata.updatedAt) < Date.parse(current.metadata.updatedAt)
      : metadata.generation !== 1 || metadata.version !== 1
  ) {
    return undefined;
  }
  return alias;
}

function decodeObservedAlias(
  value: unknown,
  scope: ServiceAliasReadScope,
  name: string,
): ServiceAliasResource | undefined {
  if (!isRecord(value) || !isRecord(value.metadata)) return undefined;
  const metadata = value.metadata;
  if (
    !boundedString(metadata.id, 36, patterns.aliasId) ||
    metadata.isolationDomainId !== scope.isolationDomainId ||
    !isPositiveInteger(metadata.generation) ||
    !isPositiveInteger(metadata.version) ||
    !isTimestamp(metadata.createdAt) ||
    !isTimestamp(metadata.updatedAt) ||
    Date.parse(metadata.updatedAt) < Date.parse(metadata.createdAt) ||
    !boundedString(metadata.createdBy, 128) ||
    value.serviceId !== scope.serviceId ||
    value.name !== name ||
    !boundedString(value.revisionId, 36, patterns.revisionId)
  ) {
    return undefined;
  }
  return {
    metadata: {
      createdAt: metadata.createdAt,
      createdBy: metadata.createdBy,
      generation: metadata.generation,
      id: metadata.id,
      isolationDomainId: scope.isolationDomainId,
      updatedAt: metadata.updatedAt,
      version: metadata.version,
    },
    name,
    revisionId: value.revisionId,
    serviceId: scope.serviceId,
  };
}

function responseFailure(error: ErrorEnvelope | undefined, status: number): ServiceAliasFailure {
  const problem = error?.error;
  if (
    problem &&
    boundedString(problem.code, 64, patterns.errorCode) &&
    boundedString(problem.message, 512) &&
    boundedString(problem.correlationId, 128) &&
    typeof problem.retryable === "boolean"
  ) {
    return {
      code: problem.code,
      correlationId: problem.correlationId,
      message: problem.message,
      retryable: problem.retryable,
      status,
    };
  }
  return {
    code: "WORKBENCH_INVALID_RESPONSE",
    message: "DataGround returned an alias response the Workbench could not interpret.",
    retryable: false,
    status,
  };
}

function failure(
  code: string,
  message: string,
  retryable = false,
  outcomeUnknown = false,
): ServiceAliasAssignResult {
  return { error: { code, message, outcomeUnknown, retryable }, ok: false };
}

export async function assignServiceAlias(
  client: DataGroundClient,
  revision: PublishedServiceRevisionResource,
  name: string,
  current: ServiceAliasResource | undefined,
  idempotencyKey: string,
): Promise<ServiceAliasAssignResult> {
  if (
    !isServiceAliasAssignmentScopeValid(revision, name, current) ||
    !patterns.idempotencyKey.test(idempotencyKey) ||
    current?.revisionId === revision.metadata.id
  ) {
    return failure(
      "WORKBENCH_INVALID_ALIAS_REQUEST",
      "The published revision, alias scope, expected version, or request identifier is invalid.",
    );
  }
  try {
    const { data, error, response } = await client.PUT(assignPath, {
      body: {
        expectedVersion: current?.metadata.version ?? 0,
        revisionId: revision.metadata.id,
      },
      params: {
        header: { "Idempotency-Key": idempotencyKey },
        path: {
          alias: name,
          isolationDomainId: revision.metadata.isolationDomainId,
          serviceId: revision.serviceId,
        },
      },
    });
    if (!data) return { error: responseFailure(error, response.status), ok: false };
    const alias = decodeAlias(data, revision, name, current);
    return alias
      ? { alias, ok: true }
      : failure(
          "WORKBENCH_ALIAS_SCOPE_MISMATCH",
          "DataGround returned alias state outside the requested scope or contract.",
        );
  } catch {
    return failure(
      "WORKBENCH_ALIAS_ASSIGNMENT_UNCONFIRMED",
      "The Workbench could not confirm whether DataGround changed the service route.",
      true,
      true,
    );
  }
}

export async function readServiceAlias(
  client: DataGroundClient,
  scope: ServiceAliasReadScope,
  name: string,
): Promise<ServiceAliasReadResult> {
  if (
    !patterns.isolationDomainId.test(scope.isolationDomainId) ||
    !patterns.serviceId.test(scope.serviceId) ||
    !boundedString(name, 63, patterns.aliasName)
  ) {
    return {
      error: {
        code: "WORKBENCH_INVALID_ALIAS_READ_REQUEST",
        message: "The service or alias scope is invalid.",
        retryable: false,
      },
      ok: false,
    };
  }
  try {
    const { data, error, response } = await client.GET(assignPath, {
      params: {
        path: {
          alias: name,
          isolationDomainId: scope.isolationDomainId,
          serviceId: scope.serviceId,
        },
      },
    });
    if (!data) {
      const problem = responseFailure(error, response.status);
      return response.status === 404 && problem.code === "SERVICE_ALIAS_NOT_FOUND"
        ? { ok: true }
        : { error: problem, ok: false };
    }
    const alias = decodeObservedAlias(data, scope, name);
    return alias
      ? { alias, ok: true }
      : {
          error: {
            code: "WORKBENCH_ALIAS_READ_SCOPE_MISMATCH",
            message: "DataGround returned alias state outside the requested scope or contract.",
            retryable: false,
          },
          ok: false,
        };
  } catch {
    return {
      error: {
        code: "WORKBENCH_ALIAS_READ_UNAVAILABLE",
        message: "The Workbench could not reach DataGround to read the service route.",
        retryable: true,
      },
      ok: false,
    };
  }
}
