import type { DataGroundClient } from "../contracts/client";
import type { components } from "../contracts/openapi.gen";
import type { InvocationReference } from "./client";

export type WorkspaceRetention = components["schemas"]["WorkspaceRetention"];
export type RetentionRead =
  | { kind: "available"; value: WorkspaceRetention }
  | { kind: "missing" }
  | { kind: "error"; message: string };

export async function readWorkspaceRetention(
  client: DataGroundClient,
  reference: InvocationReference,
  signal?: AbortSignal,
): Promise<RetentionRead> {
  try {
    const { data, response } = await client.GET(
      "/v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/workspace-retention",
      { params: { path: reference }, signal, cache: "no-store" },
    );
    if (response.status === 404) return { kind: "missing" };
    if (response.status === 403)
      return { kind: "error", message: "You do not have access to workspace retention status." };
    if (response.status === 401)
      return { kind: "error", message: "Sign in again to check workspace retention status." };
    if (
      !response.ok ||
      !data ||
      data.schemaVersion !== "dataground.invocation-workspace-retention/v1" ||
      data.isolationDomainId !== reference.isolationDomainId ||
      data.invocationId !== reference.invocationId ||
      !["not_observed", "stop_unconfirmed", "export_pending", "recorded"].includes(data.state) ||
      !["none", "review_required", "requested"].includes(data.recovery) ||
      (data.state === "recorded"
        ? typeof data.artifactId !== "string" ||
          !/^art_[0-9a-z]{20,32}$/u.test(data.artifactId) ||
          typeof data.recordedAt !== "string" ||
          !Number.isFinite(Date.parse(data.recordedAt))
        : data.artifactId !== undefined || data.recordedAt !== undefined)
    ) {
      return {
        kind: "error",
        message: "Workspace retention status could not be confirmed. Refresh to check again.",
      };
    }
    return {
      kind: "available",
      value: {
        schemaVersion: data.schemaVersion,
        isolationDomainId: data.isolationDomainId,
        invocationId: data.invocationId,
        state: data.state,
        recovery: data.recovery,
        ...(data.state === "recorded"
          ? { artifactId: data.artifactId, recordedAt: data.recordedAt }
          : {}),
      },
    };
  } catch {
    return {
      kind: "error",
      message: "Workspace retention status could not be confirmed. Refresh to check again.",
    };
  }
}
