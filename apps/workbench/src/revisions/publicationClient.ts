import type { DataGroundClient } from "../contracts/client";
import type { components } from "../contracts/openapi.gen";
import {
  readServiceRevision,
  type ServiceRevisionFailure,
  type ServiceRevisionMetadata,
  type ServiceRevisionResource,
} from "./client";

type ErrorEnvelope = components["schemas"]["ErrorEnvelope"];

export interface PublishedServiceRevisionResource {
  inputSchema?: Record<string, unknown>;
  metadata: ServiceRevisionMetadata;
  outputSchema?: Record<string, unknown>;
  publishedAt: string;
  requiredCapabilities: string[];
  revisionNumber: number;
  runtimeProfile: string;
  serviceId: string;
  state: "published";
}

export type ServiceRevisionPublishResult =
  | { ok: true; revision: PublishedServiceRevisionResource; operation?: never }
  | { ok: true; operation: PublicationOperationReference; revision?: never }
  | { error: ServiceRevisionFailure; ok: false };

const publishPath =
  "/v1/isolation-domains/{isolationDomainId}/service-revisions/{revisionId}/actions/publish";
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

function normalizeSchema(value: unknown): Record<string, unknown> | undefined {
  if (!isRecord(value) || !isJSONValue(value)) return undefined;
  try {
    const parsed: unknown = JSON.parse(JSON.stringify(value));
    return isRecord(parsed) ? parsed : undefined;
  } catch {
    return undefined;
  }
}

export function isPublishableServiceRevision(revision: ServiceRevisionResource): boolean {
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
    revision.state === "draft" &&
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
    (revision.inputSchema === undefined || normalizeSchema(revision.inputSchema) !== undefined) &&
    (revision.outputSchema === undefined || normalizeSchema(revision.outputSchema) !== undefined)
  );
}

function schemaMatches(actual: unknown, expected: Record<string, unknown> | undefined): boolean {
  if (expected === undefined) return actual === undefined;
  return isRecord(actual) && canonicalJSON(actual) === canonicalJSON(expected);
}

function decodePublishedRevision(
  value: unknown,
  draft: ServiceRevisionResource,
  expectedGeneration = draft.metadata.generation,
): PublishedServiceRevisionResource | undefined {
  if (!isRecord(value) || !isRecord(value.metadata)) return undefined;
  const metadata = value.metadata;
  const inputSchema =
    value.inputSchema === undefined ? undefined : normalizeSchema(value.inputSchema);
  const outputSchema =
    value.outputSchema === undefined ? undefined : normalizeSchema(value.outputSchema);
  if (
    metadata.id !== draft.metadata.id ||
    metadata.isolationDomainId !== draft.metadata.isolationDomainId ||
    metadata.generation !== expectedGeneration ||
    metadata.version !== draft.metadata.version + 1 ||
    metadata.createdAt !== draft.metadata.createdAt ||
    metadata.createdBy !== draft.metadata.createdBy ||
    !isTimestamp(metadata.updatedAt) ||
    value.serviceId !== draft.serviceId ||
    value.revisionNumber !== draft.revisionNumber ||
    value.state !== "published" ||
    value.runtimeProfile !== draft.runtimeProfile ||
    !Array.isArray(value.requiredCapabilities) ||
    value.requiredCapabilities.length !== draft.requiredCapabilities.length ||
    value.requiredCapabilities.some(
      (capability, index) => capability !== draft.requiredCapabilities[index],
    ) ||
    (value.inputSchema !== undefined && inputSchema === undefined) ||
    (value.outputSchema !== undefined && outputSchema === undefined) ||
    !schemaMatches(inputSchema, draft.inputSchema) ||
    !schemaMatches(outputSchema, draft.outputSchema) ||
    !isTimestamp(value.publishedAt) ||
    Date.parse(metadata.updatedAt) < Date.parse(draft.metadata.updatedAt) ||
    Date.parse(value.publishedAt) < Date.parse(draft.metadata.updatedAt) ||
    Date.parse(value.publishedAt) > Date.parse(metadata.updatedAt)
  ) {
    return undefined;
  }
  return {
    ...(inputSchema === undefined ? undefined : { inputSchema }),
    metadata: {
      createdAt: draft.metadata.createdAt,
      createdBy: draft.metadata.createdBy,
      generation: expectedGeneration,
      id: draft.metadata.id,
      isolationDomainId: draft.metadata.isolationDomainId,
      updatedAt: metadata.updatedAt,
      version: metadata.version,
    },
    ...(outputSchema === undefined ? undefined : { outputSchema }),
    publishedAt: value.publishedAt,
    requiredCapabilities: [...draft.requiredCapabilities],
    revisionNumber: draft.revisionNumber,
    runtimeProfile: draft.runtimeProfile,
    serviceId: draft.serviceId,
    state: "published",
  };
}

export function isPublishedServiceRevisionForDraft(
  revision: PublishedServiceRevisionResource,
  draft: ServiceRevisionResource,
): boolean {
  return decodePublishedRevision(revision, draft) !== undefined;
}

function failedResult(
  error: ErrorEnvelope | undefined,
  status: number,
): Extract<ServiceRevisionPublishResult, { ok: false }> {
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
      message: "DataGround returned a publication response the Workbench could not interpret.",
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
): Extract<ServiceRevisionPublishResult, { ok: false }> {
  return { error: { code, message, outcomeUnknown, retryable }, ok: false };
}

export async function publishServiceRevision(
  client: DataGroundClient,
  draft: ServiceRevisionResource,
  idempotencyKey: string,
): Promise<ServiceRevisionPublishResult> {
  if (!isPublishableServiceRevision(draft) || !patterns.idempotencyKey.test(idempotencyKey)) {
    return failure(
      "WORKBENCH_INVALID_PUBLICATION_REQUEST",
      "The revision draft, expected version, or request identifier is invalid.",
    );
  }
  try {
    const { data, error, response } = await client.POST(publishPath, {
      body: { expectedVersion: draft.metadata.version },
      params: {
        header: { "Idempotency-Key": idempotencyKey },
        path: {
          isolationDomainId: draft.metadata.isolationDomainId,
          revisionId: draft.metadata.id,
        },
      },
    });
    if (!data) return failedResult(error, response.status);
    if (response.status === 202) {
      const operation = decodePublicationOperation(data, draft, false);
      return operation
        ? { ok: true, operation }
        : failure(
            "WORKBENCH_PUBLICATION_OPERATION_UNBOUND",
            "DataGround accepted asynchronous publication without an operation reference the Workbench can verify.",
            false,
            true,
          );
    }
    if (response.status !== 200) return failedResult(error, response.status);
    const revision = decodePublishedRevision(data, draft);
    return revision
      ? { ok: true, revision }
      : failure(
          "WORKBENCH_PUBLICATION_SCOPE_MISMATCH",
          "DataGround returned published revision state outside the requested scope or contract.",
        );
  } catch {
    return failure(
      "WORKBENCH_PUBLICATION_UNCONFIRMED",
      "The Workbench could not confirm whether DataGround published the revision.",
      true,
      true,
    );
  }
}

export interface PublicationOperationReference {
  id: string;
  isolationDomainId: string;
  resourceId?: string;
  createdAt: string;
  updatedAt: string;
  version: number;
  generation: number;
  correlationId: string;
  state: "queued" | "validating" | "applying" | "observing" | "published" | "failed" | "cancelled";
}

function decodePublicationOperation(
  value: unknown,
  draft: ServiceRevisionResource,
  requireBinding: boolean,
): PublicationOperationReference | undefined {
  if (!isRecord(value) || !isRecord(value.metadata)) return undefined;
  const metadata = value.metadata;
  const states = [
    "queued",
    "validating",
    "applying",
    "observing",
    "published",
    "failed",
    "cancelled",
  ];
  if (
    !boundedString(metadata.id, 35, /^op_[0-9a-z]{20,32}$/u) ||
    metadata.isolationDomainId !== draft.metadata.isolationDomainId ||
    value.kind !== "service-publication" ||
    typeof value.command !== "string" ||
    !["publish", "repair", "cancel"].includes(value.command) ||
    value.desiredState !== (value.command === "cancel" ? "cancelled" : "published") ||
    value.stateMachineVersion !== 1 ||
    !Number.isSafeInteger(value.attempt) ||
    (value.attempt as number) < 0 ||
    !isPositiveInteger(metadata.version) ||
    !isPositiveInteger(metadata.generation) ||
    !boundedString(metadata.createdBy, 128) ||
    !isTimestamp(metadata.createdAt) ||
    !isTimestamp(metadata.updatedAt) ||
    Date.parse(metadata.updatedAt) < Date.parse(metadata.createdAt) ||
    !boundedString(value.correlationId, 128) ||
    typeof value.observedState !== "string" ||
    !states.includes(value.observedState) ||
    (value.resourceId !== undefined && value.resourceId !== draft.metadata.id) ||
    (requireBinding && value.resourceId !== draft.metadata.id)
  )
    return undefined;
  return {
    id: metadata.id,
    isolationDomainId: draft.metadata.isolationDomainId,
    ...(value.resourceId === undefined ? {} : { resourceId: draft.metadata.id }),
    createdAt: metadata.createdAt,
    updatedAt: metadata.updatedAt,
    version: metadata.version,
    generation: metadata.generation,
    correlationId: value.correlationId,
    state: value.observedState as PublicationOperationReference["state"],
  };
}

export type PublicationObservationResult =
  | {
      ok: true;
      operation: PublicationOperationReference;
      revision?: PublishedServiceRevisionResource;
    }
  | { ok: false; error: ServiceRevisionFailure };

export async function observeServiceRevisionPublication(
  client: DataGroundClient,
  draft: ServiceRevisionResource,
  accepted: PublicationOperationReference,
): Promise<PublicationObservationResult> {
  if (
    !isPublishableServiceRevision(draft) ||
    !/^op_[0-9a-z]{20,32}$/u.test(accepted.id) ||
    !isPositiveInteger(accepted.version) ||
    !isPositiveInteger(accepted.generation) ||
    !isTimestamp(accepted.createdAt) ||
    !isTimestamp(accepted.updatedAt) ||
    Date.parse(accepted.updatedAt) < Date.parse(accepted.createdAt) ||
    accepted.isolationDomainId !== draft.metadata.isolationDomainId ||
    (accepted.resourceId !== undefined && accepted.resourceId !== draft.metadata.id)
  )
    return failure(
      "WORKBENCH_INVALID_PUBLICATION_OBSERVATION",
      "The publication observation scope is invalid.",
    );
  try {
    const { data, error, response } = await client.GET(
      "/v1/isolation-domains/{isolationDomainId}/operations/{operationId}",
      {
        params: {
          path: { isolationDomainId: draft.metadata.isolationDomainId, operationId: accepted.id },
        },
      },
    );
    if (response.status !== 200) return failedResult(error, response.status);
    const operation = decodePublicationOperation(data, draft, true);
    if (
      !operation ||
      operation.id !== accepted.id ||
      Date.parse(operation.createdAt) !== Date.parse(accepted.createdAt) ||
      operation.version < accepted.version ||
      operation.generation < accepted.generation ||
      Date.parse(operation.updatedAt) < Date.parse(accepted.updatedAt)
    )
      return failure(
        "WORKBENCH_PUBLICATION_OPERATION_MISMATCH",
        "DataGround returned an operation outside the expected publication scope or contract.",
      );
    if (operation.state !== "published") return { ok: true, operation };
    const exact = await readServiceRevision(client, {
      isolationDomainId: draft.metadata.isolationDomainId,
      serviceId: draft.serviceId,
      revisionId: draft.metadata.id,
    });
    if (!exact.ok) return exact;
    const revision = decodePublishedRevision(exact.revision, draft, draft.metadata.generation + 1);
    if (!revision)
      return failure(
        "WORKBENCH_PUBLICATION_REVISION_MISMATCH",
        "The published operation could not be confirmed against the exact revision definition.",
      );
    return { ok: true, operation, revision };
  } catch {
    return failure(
      "WORKBENCH_PUBLICATION_OBSERVATION_UNAVAILABLE",
      "The Workbench could not read current publication state. Check the accepted operation again.",
      true,
    );
  }
}
