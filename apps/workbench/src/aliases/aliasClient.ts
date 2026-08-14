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
  if (!isRecord(value) || !isRecord(value.metadata)) return undefined;
  const metadata = value.metadata;
  if (
    !boundedString(metadata.id, 36, patterns.aliasId) ||
    metadata.isolationDomainId !== revision.metadata.isolationDomainId ||
    !isPositiveInteger(metadata.generation) ||
    !isPositiveInteger(metadata.version) ||
    !isTimestamp(metadata.createdAt) ||
    !isTimestamp(metadata.updatedAt) ||
    Date.parse(metadata.updatedAt) < Date.parse(metadata.createdAt) ||
    !boundedString(metadata.createdBy, 128) ||
    value.serviceId !== revision.serviceId ||
    value.name !== name ||
    value.revisionId !== revision.metadata.id
  ) {
    return undefined;
  }
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
  return {
    metadata: {
      createdAt: metadata.createdAt,
      createdBy: metadata.createdBy,
      generation: metadata.generation,
      id: metadata.id,
      isolationDomainId: revision.metadata.isolationDomainId,
      updatedAt: metadata.updatedAt,
      version: metadata.version,
    },
    name,
    revisionId: revision.metadata.id,
    serviceId: revision.serviceId,
  };
}

function failedResult(
  error: ErrorEnvelope | undefined,
  status: number,
): Extract<ServiceAliasAssignResult, { ok: false }> {
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
      message: "DataGround returned an alias response the Workbench could not interpret.",
      retryable: false,
      status,
    },
    ok: false,
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
    if (!data) return failedResult(error, response.status);
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
