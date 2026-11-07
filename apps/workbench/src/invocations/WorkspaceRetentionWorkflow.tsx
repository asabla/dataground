import { Button, StatusBadge } from "@dataground/ui";
import { type ReactNode, useEffect, useId, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import type { InvocationReference } from "./client";
import { useWorkspaceRetentionRecovery } from "./useWorkspaceRetentionRecovery";
import { type RetentionRead, readWorkspaceRetention } from "./workspaceRetention";

export function WorkspaceRetentionStatus({
  result,
  loading,
  onRefresh,
  onInspectArtifact,
  recovery,
  readUnavailable = false,
}: {
  recovery?: ReactNode;
  readUnavailable?: boolean;
  result?: RetentionRead;
  loading: boolean;
  onRefresh: () => void;
  onInspectArtifact?: (artifactId: string) => void;
}) {
  const titleId = useId();
  if (result?.kind === "missing" && !recovery) return null;
  const value = result?.kind === "available" ? result.value : undefined;
  return (
    <section aria-labelledby={titleId} className="workspace-retention">
      <div className="workbench-page-heading workbench-page-heading--compact">
        <h2 id={titleId}>Workspace changes</h2>
        <Button variant="quiet" isDisabled={loading || readUnavailable} onPress={onRefresh}>
          Refresh retention status
        </Button>
      </div>
      {loading && <p role="status">Checking workspace retention…</p>}
      {readUnavailable && <p>Refresh run state to check workspace retention.</p>}
      {result?.kind === "missing" && (
        <p>
          Workspace retention metadata is no longer available. The request receipt below is
          historical.
        </p>
      )}
      {result?.kind === "error" && <p role="alert">{result.message}</p>}
      {value && (
        <>
          <StatusBadge
            tone={
              value.recovery === "review_required" || value.state === "export_pending"
                ? "warning"
                : "neutral"
            }
          >
            {
              {
                not_observed: "Execution not observed",
                stop_unconfirmed: "Stop not confirmed",
                export_pending: "Changes not yet retained",
                recorded: "Changes recorded",
              }[value.state]
            }
          </StatusBadge>
          {value.recovery === "review_required" && (
            <p role="alert">
              Cancellation reached its deadline.{" "}
              {value.recoveryAccess === "allowed"
                ? "Review workspace retention before cleanup."
                : "An operator must review workspace retention before cleanup."}
            </p>
          )}
          {value.recovery === "requested" && (
            <p role="status">
              Recovery was requested. The worker must verify current access and retained changes
              before cancellation can finish.
            </p>
          )}
          {value.state === "not_observed" && (
            <p>No execution has been recorded for this workspace.</p>
          )}
          {value.state === "stop_unconfirmed" && (
            <p>The runtime stop has not been confirmed. Final changes are not yet recorded.</p>
          )}
          {value.state === "export_pending" && (
            <p>
              The runtime stop is recorded. Keep the workspace until its final changes are retained.
            </p>
          )}
          {value.state === "recorded" && (
            <>
              <p>
                Change evidence was recorded{" "}
                {value.recordedAt && (
                  <time dateTime={value.recordedAt}>
                    {new Date(value.recordedAt).toLocaleString()}
                  </time>
                )}
                . File access and availability are checked separately.
              </p>
              <Button
                variant="quiet"
                isDisabled={!onInspectArtifact || loading}
                onPress={() => {
                  if (value.artifactId) onInspectArtifact?.(value.artifactId);
                }}
              >
                Inspect workspace changes
              </Button>
            </>
          )}
        </>
      )}
      {recovery}
    </section>
  );
}

export function WorkspaceRetentionWorkflow({
  client,
  reference,
  revision,
  active,
  confirmed = true,
  onInspectArtifact,
  onRecoveryAccepted,
}: {
  onRecoveryAccepted?: () => void;
  client: DataGroundClient;
  reference: InvocationReference;
  revision: number;
  active: boolean;
  confirmed?: boolean;
  onInspectArtifact?: (artifactId: string) => void;
}) {
  const { isolationDomainId, invocationId } = reference;
  const key = `${isolationDomainId}:${invocationId}:${revision}`;
  const [refresh, setRefresh] = useState(0);
  const [state, setState] = useState<{
    client: DataGroundClient;
    key: string;
    refresh: number;
    result: RetentionRead;
  }>();
  useEffect(() => {
    if (!active || !confirmed) {
      setState(undefined);
      return;
    }
    const controller = new AbortController();
    let current = true;
    void readWorkspaceRetention(
      client,
      { isolationDomainId, invocationId },
      controller.signal,
    ).then((result) => {
      if (current) setState({ client, key, refresh, result });
    });
    return () => {
      current = false;
      controller.abort();
    };
  }, [client, isolationDomainId, invocationId, key, refresh, active, confirmed]);
  const result =
    confirmed && state?.client === client && state.key === key && state.refresh === refresh
      ? state.result
      : undefined;
  const recovery = useWorkspaceRetentionRecovery({
    client,
    reference,
    version: revision,
    active,
    status: result,
    onAccepted: onRecoveryAccepted,
  });
  if (!active || (!confirmed && !recovery)) return null;
  return (
    <WorkspaceRetentionStatus
      recovery={recovery}
      result={result}
      loading={confirmed && !result}
      readUnavailable={!confirmed}
      onRefresh={() => setRefresh((value) => value + 1)}
      onInspectArtifact={onInspectArtifact}
    />
  );
}
