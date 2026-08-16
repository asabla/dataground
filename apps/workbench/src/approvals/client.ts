import type { DataGroundClient } from "../contracts/client";
import type { components } from "../contracts/openapi.gen";

export type InvocationApproval = components["schemas"]["InvocationApproval"];
type ErrorEnvelope = components["schemas"]["ErrorEnvelope"];

export interface InvocationApprovalReference {
  approvalId: string;
  invocationId: string;
  isolationDomainId: string;
}

export interface ApprovalFailure {
  code: string;
  correlationId?: string;
  message: string;
  retryable: boolean;
  status?: number;
}

export type ApprovalResult =
  | { approval: InvocationApproval; ok: true }
  | { error: ApprovalFailure; ok: false };

const approvalPath =
  "/v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/approvals/{approvalId}";
const patterns = {
  approvalId: /^apr_[0-9a-z]{20,32}$/u,
  idempotencyKey: /^[A-Za-z0-9._:-]{8,128}$/u,
  invocationId: /^inv_[0-9a-z]{20,32}$/u,
  isolationDomainId: /^iso_[0-9a-z]{20,32}$/u,
  timestamp: /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/u,
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isTimestamp(value: unknown): value is string {
  return (
    typeof value === "string" && patterns.timestamp.test(value) && !Number.isNaN(Date.parse(value))
  );
}

function validReference(reference: InvocationApprovalReference): boolean {
  return (
    patterns.approvalId.test(reference.approvalId) &&
    patterns.invocationId.test(reference.invocationId) &&
    patterns.isolationDomainId.test(reference.isolationDomainId)
  );
}

function invalidRequestResult(message: string): ApprovalResult {
  return {
    error: { code: "WORKBENCH_INVALID_REQUEST", message, retryable: false },
    ok: false,
  };
}

function failedResult(error: ErrorEnvelope | undefined, status: number): ApprovalResult {
  const problem = error?.error;
  if (problem) {
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
      message: "DataGround returned a response the Workbench could not interpret.",
      retryable: false,
      status,
    },
    ok: false,
  };
}

function unavailableResult(): ApprovalResult {
  return {
    error: {
      code: "WORKBENCH_NETWORK_UNAVAILABLE",
      message: "The Workbench could not reach DataGround. The command outcome may be unknown.",
      retryable: true,
    },
    ok: false,
  };
}

function matchedApprovalResult(
  value: unknown,
  reference: InvocationApprovalReference,
): ApprovalResult {
  if (!isRecord(value)) {
    return invalidRequestResult(
      "DataGround returned approval data the Workbench could not interpret.",
    );
  }
  if (
    value.schemaVersion !== "dataground.invocation-approval/v1" ||
    value.id !== reference.approvalId ||
    value.invocationId !== reference.invocationId ||
    value.isolationDomainId !== reference.isolationDomainId ||
    !["process.execute", "workspace.change"].includes(String(value.requestedAction)) ||
    !["pending", "resolved", "delivering", "delivered"].includes(String(value.state)) ||
    !Number.isSafeInteger(value.version) ||
    (value.version as number) < 1 ||
    !isTimestamp(value.createdAt) ||
    !isTimestamp(value.updatedAt) ||
    (value.decision !== undefined && !["approve", "deny"].includes(String(value.decision))) ||
    (value.resolvedBy !== undefined &&
      (typeof value.resolvedBy !== "string" ||
        value.resolvedBy.length < 1 ||
        value.resolvedBy.length > 256)) ||
    (value.resolvedAt !== undefined && !isTimestamp(value.resolvedAt))
  ) {
    return {
      error: {
        code: "WORKBENCH_SCOPE_MISMATCH",
        message: "DataGround returned approval data outside the requested scope.",
        retryable: false,
      },
      ok: false,
    };
  }
  return { approval: value as unknown as InvocationApproval, ok: true };
}

export async function readInvocationApproval(
  client: DataGroundClient,
  reference: InvocationApprovalReference,
): Promise<ApprovalResult> {
  if (!validReference(reference)) {
    return invalidRequestResult("The invocation approval reference is invalid.");
  }
  try {
    const { data, error, response } = await client.GET(approvalPath, {
      params: { path: reference },
    });
    return data ? matchedApprovalResult(data, reference) : failedResult(error, response.status);
  } catch {
    return unavailableResult();
  }
}

export async function resolveInvocationApproval(
  client: DataGroundClient,
  reference: InvocationApprovalReference,
  decision: "approve" | "deny",
  idempotencyKey: string,
): Promise<ApprovalResult> {
  if (
    !validReference(reference) ||
    !["approve", "deny"].includes(decision) ||
    !patterns.idempotencyKey.test(idempotencyKey)
  ) {
    return invalidRequestResult("The invocation approval decision request is invalid.");
  }
  try {
    const { data, error, response } = await client.POST(approvalPath, {
      body: { decision, expectedVersion: 1 },
      params: {
        header: { "Idempotency-Key": idempotencyKey },
        path: reference,
      },
    });
    return data ? matchedApprovalResult(data, reference) : failedResult(error, response.status);
  } catch {
    return unavailableResult();
  }
}
