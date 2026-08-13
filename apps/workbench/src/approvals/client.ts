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
  approval: InvocationApproval,
  reference: InvocationApprovalReference,
): ApprovalResult {
  if (
    approval.schemaVersion !== "dataground.invocation-approval/v1" ||
    approval.id !== reference.approvalId ||
    approval.invocationId !== reference.invocationId ||
    approval.isolationDomainId !== reference.isolationDomainId
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
  return { approval, ok: true };
}

export async function readInvocationApproval(
  client: DataGroundClient,
  reference: InvocationApprovalReference,
): Promise<ApprovalResult> {
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
