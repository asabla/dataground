import type { DataGroundClient } from "../contracts/client";
import type { components } from "../contracts/openapi.gen";

type ErrorEnvelope = components["schemas"]["ErrorEnvelope"];

interface ResourceMetadata {
  createdAt: string;
  createdBy: string;
  generation: number;
  id: string;
  isolationDomainId: string;
  updatedAt: string;
  version: number;
}

interface SafeError {
  code: string;
  correlationId: string;
  message: string;
  retryable: boolean;
}

export interface InvocationStatusResource {
  alias: string;
  artifactIds: string[];
  completedAt?: string;
  correlationId: string;
  error?: SafeError;
  metadata: ResourceMetadata;
  operationId: string;
  revisionId: string;
  serviceId: string;
  state: string;
  usage?: {
    inputTokens: number;
    outputTokens: number;
    totalTokens: number;
  };
}

export interface InvocationOperationResource {
  attempt: number;
  command: string;
  correlationId: string;
  deadlineAt?: string;
  desiredState: string;
  dueAt?: string;
  error?: SafeError;
  errorClassification?: string;
  kind: string;
  metadata: ResourceMetadata;
  observedState: string;
  stateMachineVersion: number;
}

export interface InvocationReference {
  invocationId: string;
  isolationDomainId: string;
}

export interface AgentServiceInvocationTarget {
  isolationDomainId: string;
  serviceId: string;
}

export interface InvocationFailure {
  code: string;
  correlationId?: string;
  message: string;
  outcomeUnknown?: boolean;
  retryable: boolean;
  status?: number;
}

export type InvocationStatusResult =
  | {
      invocation: InvocationStatusResource;
      ok: true;
      operation?: InvocationOperationResource;
      operationError?: InvocationFailure;
    }
  | { error: InvocationFailure; ok: false };

const invocationPath = "/v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}";
const invocationCreatePath =
  "/v1/isolation-domains/{isolationDomainId}/agent-services/{serviceId}/invocations";
const cancellationPath =
  "/v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/actions/cancel";
const operationPath = "/v1/isolation-domains/{isolationDomainId}/operations/{operationId}";
const patterns = {
  alias: /^[a-z](?:[a-z0-9-]*[a-z0-9])?$/u,
  artifactId: /^art_[0-9a-z]{20,32}$/u,
  errorCode: /^[A-Z][A-Z0-9_]{2,63}$/u,
  idempotencyKey: /^[A-Za-z0-9._:-]{8,128}$/u,
  invocationId: /^inv_[0-9a-z]{20,32}$/u,
  isolationDomainId: /^iso_[0-9a-z]{20,32}$/u,
  operationId: /^op_[0-9a-z]{20,32}$/u,
  revisionId: /^rev_[0-9a-z]{20,32}$/u,
  serviceId: /^svc_[0-9a-z]{20,32}$/u,
  state: /^[a-z][a-z0-9-]{0,63}$/u,
  timestamp: /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/u,
};

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
    typeof value === "string" && patterns.timestamp.test(value) && !Number.isNaN(Date.parse(value))
  );
}

function isNonnegativeInteger(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) >= 0;
}

function isPositiveInteger(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) >= 1;
}

function decodeMetadata(
  value: unknown,
  expectedId: string,
  expectedDomainId: string,
): ResourceMetadata | undefined {
  if (
    !isRecord(value) ||
    value.id !== expectedId ||
    value.isolationDomainId !== expectedDomainId ||
    !isPositiveInteger(value.generation) ||
    !isPositiveInteger(value.version) ||
    !isTimestamp(value.createdAt) ||
    !isTimestamp(value.updatedAt) ||
    !boundedString(value.createdBy, 128)
  ) {
    return undefined;
  }
  return {
    createdAt: value.createdAt,
    createdBy: value.createdBy,
    generation: value.generation,
    id: value.id,
    isolationDomainId: value.isolationDomainId,
    updatedAt: value.updatedAt,
    version: value.version,
  };
}

function decodeSafeError(value: unknown): SafeError | undefined {
  if (
    !isRecord(value) ||
    !boundedString(value.code, 64, patterns.errorCode) ||
    !boundedString(value.message, 512) ||
    !boundedString(value.correlationId, 128) ||
    typeof value.retryable !== "boolean"
  ) {
    return undefined;
  }
  return {
    code: value.code,
    correlationId: value.correlationId,
    message: value.message,
    retryable: value.retryable,
  };
}

function decodeUsage(value: unknown): InvocationStatusResource["usage"] | undefined {
  if (
    !isRecord(value) ||
    !isNonnegativeInteger(value.inputTokens) ||
    !isNonnegativeInteger(value.outputTokens) ||
    !isNonnegativeInteger(value.totalTokens)
  ) {
    return undefined;
  }
  return {
    inputTokens: value.inputTokens,
    outputTokens: value.outputTokens,
    totalTokens: value.totalTokens,
  };
}

function decodeInvocation(
  value: unknown,
  reference: InvocationReference,
): InvocationStatusResource | undefined {
  if (!isRecord(value)) {
    return undefined;
  }
  const metadata = decodeMetadata(
    value.metadata,
    reference.invocationId,
    reference.isolationDomainId,
  );
  const artifactIds = Array.isArray(value.artifactIds) ? value.artifactIds : undefined;
  const usage = value.usage === undefined ? undefined : decodeUsage(value.usage);
  const error = value.error === undefined ? undefined : decodeSafeError(value.error);
  if (
    metadata === undefined ||
    !boundedString(value.serviceId, 36, patterns.serviceId) ||
    !boundedString(value.revisionId, 36, patterns.revisionId) ||
    !boundedString(value.alias, 63, patterns.alias) ||
    !boundedString(value.state, 64, patterns.state) ||
    !isRecord(value.input) ||
    (value.result !== undefined && !isRecord(value.result)) ||
    (value.usage !== undefined && usage === undefined) ||
    (value.error !== undefined && error === undefined) ||
    !boundedString(value.correlationId, 128) ||
    !boundedString(value.operationId, 35, patterns.operationId) ||
    artifactIds === undefined ||
    !artifactIds.every((id): id is string => boundedString(id, 36, patterns.artifactId)) ||
    new Set(artifactIds).size !== artifactIds.length ||
    (value.completedAt !== undefined && !isTimestamp(value.completedAt))
  ) {
    return undefined;
  }
  return {
    alias: value.alias,
    artifactIds: [...artifactIds],
    ...(value.completedAt === undefined ? undefined : { completedAt: value.completedAt }),
    correlationId: value.correlationId,
    ...(error === undefined ? undefined : { error }),
    metadata,
    operationId: value.operationId,
    revisionId: value.revisionId,
    serviceId: value.serviceId,
    state: value.state,
    ...(usage === undefined ? undefined : { usage }),
  };
}

function decodeOperation(
  value: unknown,
  invocation: InvocationStatusResource,
): InvocationOperationResource | undefined {
  if (!isRecord(value)) {
    return undefined;
  }
  const metadata = decodeMetadata(
    value.metadata,
    invocation.operationId,
    invocation.metadata.isolationDomainId,
  );
  const error = value.error === undefined ? undefined : decodeSafeError(value.error);
  if (
    metadata === undefined ||
    !boundedString(value.kind, 64) ||
    !boundedString(value.command, 64) ||
    !boundedString(value.desiredState, 64, patterns.state) ||
    !boundedString(value.observedState, 64, patterns.state) ||
    !isPositiveInteger(value.stateMachineVersion) ||
    !isNonnegativeInteger(value.attempt) ||
    value.correlationId !== invocation.correlationId ||
    (value.dueAt !== undefined && !isTimestamp(value.dueAt)) ||
    (value.deadlineAt !== undefined && !isTimestamp(value.deadlineAt)) ||
    (value.errorClassification !== undefined &&
      !boundedString(value.errorClassification, 64, patterns.state)) ||
    (value.error !== undefined && error === undefined)
  ) {
    return undefined;
  }
  return {
    attempt: value.attempt,
    command: value.command,
    correlationId: value.correlationId,
    ...(value.deadlineAt === undefined ? undefined : { deadlineAt: value.deadlineAt }),
    desiredState: value.desiredState,
    ...(value.dueAt === undefined ? undefined : { dueAt: value.dueAt }),
    ...(error === undefined ? undefined : { error }),
    ...(value.errorClassification === undefined
      ? undefined
      : { errorClassification: value.errorClassification }),
    kind: value.kind,
    metadata,
    observedState: value.observedState,
    stateMachineVersion: value.stateMachineVersion,
  };
}

function failure(
  code: string,
  message: string,
  retryable = false,
  outcomeUnknown = false,
): InvocationStatusResult {
  return { error: { code, message, outcomeUnknown, retryable }, ok: false };
}

function failedResult(
  error: ErrorEnvelope | undefined,
  status: number,
  fallbackMessage: string,
): Extract<InvocationStatusResult, { ok: false }> {
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
      message: fallbackMessage,
      retryable: false,
      status,
    },
    ok: false,
  };
}

function validReference(reference: InvocationReference): boolean {
  return (
    patterns.invocationId.test(reference.invocationId) &&
    patterns.isolationDomainId.test(reference.isolationDomainId)
  );
}

function validTarget(target: AgentServiceInvocationTarget): boolean {
  return (
    patterns.isolationDomainId.test(target.isolationDomainId) &&
    patterns.serviceId.test(target.serviceId)
  );
}

function decodeCreatedInvocation(
  value: unknown,
  target: AgentServiceInvocationTarget,
  alias: string,
): InvocationStatusResource | undefined {
  if (!isRecord(value) || !isRecord(value.metadata) || typeof value.metadata.id !== "string") {
    return undefined;
  }
  const invocation = decodeInvocation(value, {
    invocationId: value.metadata.id,
    isolationDomainId: target.isolationDomainId,
  });
  return invocation?.serviceId === target.serviceId && invocation.alias === alias
    ? invocation
    : undefined;
}

async function readOperation(
  client: DataGroundClient,
  invocation: InvocationStatusResource,
): Promise<
  { ok: true; operation: InvocationOperationResource } | { error: InvocationFailure; ok: false }
> {
  try {
    const { data, error, response } = await client.GET(operationPath, {
      params: {
        path: {
          isolationDomainId: invocation.metadata.isolationDomainId,
          operationId: invocation.operationId,
        },
      },
    });
    if (!data) {
      return failedResult(
        error,
        response.status,
        "DataGround returned operation state the Workbench could not interpret.",
      );
    }
    const operation = decodeOperation(data, invocation);
    return operation
      ? { ok: true, operation }
      : {
          error: {
            code: "WORKBENCH_OPERATION_SCOPE_MISMATCH",
            message:
              "DataGround returned operation state outside the invocation scope or contract.",
            retryable: false,
          },
          ok: false,
        };
  } catch {
    return {
      error: {
        code: "WORKBENCH_NETWORK_UNAVAILABLE",
        message: "The Workbench could not reach DataGround to read durable operation state.",
        retryable: true,
      },
      ok: false,
    };
  }
}

async function completeSnapshot(
  client: DataGroundClient,
  invocation: InvocationStatusResource,
): Promise<Extract<InvocationStatusResult, { ok: true }>> {
  const operation = await readOperation(client, invocation);
  return operation.ok
    ? { invocation, ok: true, operation: operation.operation }
    : { invocation, ok: true, operationError: operation.error };
}

export async function readInvocationStatus(
  client: DataGroundClient,
  reference: InvocationReference,
): Promise<InvocationStatusResult> {
  if (!validReference(reference)) {
    return failure("WORKBENCH_INVALID_REFERENCE", "The invocation reference is invalid.");
  }
  try {
    const { data, error, response } = await client.GET(invocationPath, {
      params: { path: reference },
    });
    if (!data) {
      return failedResult(
        error,
        response.status,
        "DataGround returned invocation state the Workbench could not interpret.",
      );
    }
    const invocation = decodeInvocation(data, reference);
    return invocation
      ? completeSnapshot(client, invocation)
      : failure(
          "WORKBENCH_INVOCATION_SCOPE_MISMATCH",
          "DataGround returned invocation state outside the requested scope or contract.",
        );
  } catch {
    return failure(
      "WORKBENCH_NETWORK_UNAVAILABLE",
      "The Workbench could not reach DataGround to read invocation state.",
      true,
    );
  }
}

export async function invokeAgentService(
  client: DataGroundClient,
  target: AgentServiceInvocationTarget,
  alias: string,
  input: Readonly<Record<string, string>>,
  idempotencyKey: string,
): Promise<InvocationStatusResult> {
  if (
    !validTarget(target) ||
    !patterns.alias.test(alias) ||
    alias.length > 63 ||
    !isRecord(input) ||
    !Object.values(input).every((value) => typeof value === "string") ||
    !patterns.idempotencyKey.test(idempotencyKey)
  ) {
    return failure(
      "WORKBENCH_INVALID_INVOCATION_REQUEST",
      "The invocation target, alias, input, or request identifier is invalid.",
    );
  }
  try {
    const { data, error, response } = await client.POST(invocationCreatePath, {
      body: { alias, input },
      params: {
        header: { "Idempotency-Key": idempotencyKey },
        path: target,
      },
    });
    if (!data) {
      return failedResult(
        error,
        response.status,
        "DataGround returned an invocation response the Workbench could not interpret.",
      );
    }
    const invocation = decodeCreatedInvocation(data, target, alias);
    return invocation
      ? completeSnapshot(client, invocation)
      : failure(
          "WORKBENCH_INVOCATION_SCOPE_MISMATCH",
          "DataGround returned invocation state outside the requested service scope.",
        );
  } catch {
    return failure(
      "WORKBENCH_INVOCATION_UNCONFIRMED",
      "The Workbench could not confirm whether DataGround accepted the invocation.",
      true,
      true,
    );
  }
}

export async function cancelInvocation(
  client: DataGroundClient,
  reference: InvocationReference,
  idempotencyKey: string,
): Promise<InvocationStatusResult> {
  if (!validReference(reference) || !patterns.idempotencyKey.test(idempotencyKey)) {
    return failure(
      "WORKBENCH_INVALID_REFERENCE",
      "The invocation cancellation reference or request identifier is invalid.",
    );
  }
  try {
    const { data, error, response } = await client.POST(cancellationPath, {
      body: {},
      params: {
        header: { "Idempotency-Key": idempotencyKey },
        path: reference,
      },
    });
    if (!data) {
      return failedResult(
        error,
        response.status,
        "DataGround returned a cancellation response the Workbench could not interpret.",
      );
    }
    const invocation = decodeInvocation(data, reference);
    return invocation
      ? completeSnapshot(client, invocation)
      : failure(
          "WORKBENCH_INVOCATION_SCOPE_MISMATCH",
          "DataGround returned cancellation state outside the requested invocation scope.",
        );
  } catch {
    return failure(
      "WORKBENCH_CANCELLATION_UNCONFIRMED",
      "The Workbench could not confirm whether DataGround accepted the cancellation.",
      true,
      true,
    );
  }
}
