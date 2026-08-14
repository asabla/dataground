import type { DataGroundClient } from "../contracts/client";
import type { components } from "../contracts/openapi.gen";

interface InvocationArtifactMetadata {
  createdAt: string;
  createdBy: string;
  generation: number;
  id: string;
  isolationDomainId: string;
  labels?: Record<string, string>;
  provenance?: {
    requestCorrelationId?: string;
    sourceRevision?: string;
  };
  updatedAt: string;
  version: number;
}

export interface InvocationArtifact {
  digest: string;
  invocationId: string;
  kind: string;
  mediaType: string;
  metadata: InvocationArtifactMetadata;
  name: string;
  sensitive: boolean;
  sizeBytes: number;
  state: string;
}
type ErrorEnvelope = components["schemas"]["ErrorEnvelope"];

export interface InvocationArtifactReference {
  artifactId: string;
  invocationId: string;
  isolationDomainId: string;
}

export interface ArtifactFailure {
  code: string;
  correlationId?: string;
  message: string;
  retryable: boolean;
  status?: number;
}

export type ArtifactResult =
  | { artifact: InvocationArtifact; ok: true }
  | { error: ArtifactFailure; ok: false };

const artifactPath =
  "/v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/artifacts/{artifactId}";
const resourcePatterns = {
  artifactId: /^art_[0-9a-z]{20,32}$/u,
  invocationId: /^inv_[0-9a-z]{20,32}$/u,
  isolationDomainId: /^iso_[0-9a-z]{20,32}$/u,
};
const digestPattern = /^sha256:[0-9a-f]{64}$/u;
const errorCodePattern = /^[A-Z][A-Z0-9_]{2,63}$/u;
const labelKeyPattern = /^[a-z][a-z0-9._/-]{0,62}$/u;
const statePattern = /^[a-z][a-z0-9-]{0,63}$/u;
const timestampPattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/u;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function boundedString(value: unknown, maximum: number, pattern?: RegExp): value is string {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value.length <= maximum &&
    (pattern === undefined || pattern.test(value))
  );
}

function isTimestamp(value: unknown): value is string {
  return (
    typeof value === "string" && timestampPattern.test(value) && !Number.isNaN(Date.parse(value))
  );
}

function isPositiveInteger(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) >= 1;
}

function isNonnegativeInteger(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) >= 0;
}

function validReference(reference: InvocationArtifactReference): boolean {
  return (
    resourcePatterns.artifactId.test(reference.artifactId) &&
    resourcePatterns.invocationId.test(reference.invocationId) &&
    resourcePatterns.isolationDomainId.test(reference.isolationDomainId)
  );
}

function validLabels(value: unknown): value is Record<string, string> | undefined {
  if (value === undefined) {
    return true;
  }
  if (!isRecord(value) || Object.keys(value).length > 32) {
    return false;
  }
  return Object.entries(value).every(
    ([key, entry]) => labelKeyPattern.test(key) && typeof entry === "string" && entry.length <= 256,
  );
}

function validProvenance(
  value: unknown,
): value is InvocationArtifactMetadata["provenance"] | undefined {
  if (value === undefined) {
    return true;
  }
  if (
    !isRecord(value) ||
    Object.keys(value).some((key) => !["sourceRevision", "requestCorrelationId"].includes(key))
  ) {
    return false;
  }
  return [value.sourceRevision, value.requestCorrelationId].every(
    (entry) => entry === undefined || boundedString(entry, 128),
  );
}

function decodeArtifact(
  value: unknown,
  reference: InvocationArtifactReference,
): InvocationArtifact | undefined {
  if (!isRecord(value) || !isRecord(value.metadata)) {
    return undefined;
  }
  const metadata = value.metadata;
  if (
    metadata.id !== reference.artifactId ||
    metadata.isolationDomainId !== reference.isolationDomainId ||
    value.invocationId !== reference.invocationId ||
    !isPositiveInteger(metadata.generation) ||
    !isPositiveInteger(metadata.version) ||
    !isTimestamp(metadata.createdAt) ||
    !isTimestamp(metadata.updatedAt) ||
    !boundedString(metadata.createdBy, 128) ||
    !validLabels(metadata.labels) ||
    !validProvenance(metadata.provenance) ||
    !boundedString(value.name, 255) ||
    !boundedString(value.kind, 64, statePattern) ||
    !boundedString(value.mediaType, 255) ||
    !isNonnegativeInteger(value.sizeBytes) ||
    !boundedString(value.digest, 71, digestPattern) ||
    !boundedString(value.state, 64, statePattern) ||
    typeof value.sensitive !== "boolean"
  ) {
    return undefined;
  }
  return {
    digest: value.digest,
    invocationId: value.invocationId,
    kind: value.kind,
    mediaType: value.mediaType,
    metadata: {
      createdAt: metadata.createdAt,
      createdBy: metadata.createdBy,
      generation: metadata.generation,
      id: metadata.id,
      isolationDomainId: metadata.isolationDomainId,
      ...(metadata.labels === undefined ? undefined : { labels: metadata.labels }),
      ...(metadata.provenance === undefined ? undefined : { provenance: metadata.provenance }),
      updatedAt: metadata.updatedAt,
      version: metadata.version,
    },
    name: value.name,
    sensitive: value.sensitive,
    sizeBytes: value.sizeBytes,
    state: value.state,
  };
}

function failure(code: string, message: string, retryable = false): ArtifactResult {
  return { error: { code, message, retryable }, ok: false };
}

function failedResult(error: ErrorEnvelope | undefined, status: number): ArtifactResult {
  const problem = error?.error;
  if (
    problem &&
    boundedString(problem.code, 64, errorCodePattern) &&
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
      message: "DataGround returned artifact metadata the Workbench could not interpret.",
      retryable: false,
      status,
    },
    ok: false,
  };
}

export async function readInvocationArtifact(
  client: DataGroundClient,
  reference: InvocationArtifactReference,
): Promise<ArtifactResult> {
  if (!validReference(reference)) {
    return failure("WORKBENCH_INVALID_REFERENCE", "The invocation artifact reference is invalid.");
  }
  try {
    const { data, error, response } = await client.GET(artifactPath, {
      params: { path: reference },
    });
    if (!data) {
      return failedResult(error, response.status);
    }
    const artifact = decodeArtifact(data, reference);
    return artifact
      ? { artifact, ok: true }
      : failure(
          "WORKBENCH_ARTIFACT_SCOPE_MISMATCH",
          "DataGround returned artifact metadata outside the requested scope or contract.",
        );
  } catch {
    return failure(
      "WORKBENCH_NETWORK_UNAVAILABLE",
      "The Workbench could not reach DataGround to read artifact metadata.",
      true,
    );
  }
}
