import type { DataGroundClient } from "../contracts/client";
import type { components } from "../contracts/openapi.gen";
import type { InvocationReference } from "./client";

export interface RetentionRecoveryAttempt {
  expectedVersion: number;
  idempotencyKey: string;
}
export type RetentionRecoveryReceipt = components["schemas"]["WorkspaceRetentionRecoveryReceipt"];
export type RetentionRecoveryResult =
  | { ok: true; receipt: RetentionRecoveryReceipt }
  | { ok: false; message: string; uncertain: boolean };

const uncertain: RetentionRecoveryResult = {
  ok: false,
  message:
    "The recovery request outcome is unknown. Retry the exact request to check its acceptance.",
  uncertain: true,
};

export async function requestWorkspaceRetentionRecovery(
  client: DataGroundClient,
  reference: InvocationReference,
  attempt: RetentionRecoveryAttempt,
): Promise<RetentionRecoveryResult> {
  if (
    !Number.isSafeInteger(attempt.expectedVersion) ||
    attempt.expectedVersion < 1 ||
    attempt.expectedVersion > 9007199254740990 ||
    !/^retention-recovery:[0-9a-f]{32}$/u.test(attempt.idempotencyKey)
  ) {
    return {
      ok: false,
      uncertain: false,
      message: "A valid reviewed run version and stable request key are required.",
    };
  }
  try {
    const { data, response } = await client.POST(
      "/v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/workspace-retention/recoveries",
      {
        params: { path: reference, header: { "Idempotency-Key": attempt.idempotencyKey } },
        body: { expectedVersion: attempt.expectedVersion },
      },
    );
    if (response.status >= 500) return uncertain;
    if (response.status === 401 || response.status === 403)
      return {
        ok: false,
        uncertain: false,
        message:
          "Your current identity cannot request retention recovery. Check access before a new request.",
      };
    if (response.status === 409)
      return {
        ok: false,
        uncertain: false,
        message:
          "The run changed or retention recovery is not available. Refresh the run before a new request.",
      };
    if (!response.ok)
      return {
        ok: false,
        uncertain: false,
        message:
          "The retention recovery request was rejected. Refresh the run before a new request.",
      };
    if (
      response.status !== 202 ||
      !data ||
      data.schemaVersion !== "dataground.workspace-retention-recovery-receipt/v1" ||
      data.isolationDomainId !== reference.isolationDomainId ||
      data.invocationId !== reference.invocationId ||
      data.state !== "requested" ||
      data.invocationVersion !== attempt.expectedVersion + 1 ||
      typeof data.deadlineAt !== "string" ||
      !Number.isFinite(Date.parse(data.deadlineAt)) ||
      typeof data.correlationId !== "string" ||
      !/^cor_[0-9a-z]{20,32}$/u.test(data.correlationId)
    )
      return uncertain;
    return {
      ok: true,
      receipt: {
        schemaVersion: data.schemaVersion,
        isolationDomainId: data.isolationDomainId,
        invocationId: data.invocationId,
        state: data.state,
        invocationVersion: data.invocationVersion,
        deadlineAt: data.deadlineAt,
        correlationId: data.correlationId,
      },
    };
  } catch {
    return uncertain;
  }
}
