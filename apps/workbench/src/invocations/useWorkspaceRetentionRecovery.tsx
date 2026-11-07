import { Button, Dialog } from "@dataground/ui";
import { useEffect, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import type { InvocationReference } from "./client";
import type { RetentionRead } from "./workspaceRetention";
import {
  type RetentionRecoveryAttempt,
  type RetentionRecoveryResult,
  requestWorkspaceRetentionRecovery,
} from "./workspaceRetentionRecovery";

// Command state lives with the retained run, independently of metadata refresh
// and visibility. Identity or scope replacement discards it and late responses.
export function useWorkspaceRetentionRecovery({
  client,
  reference,
  version,
  active,
  status,
  onAccepted,
}: {
  client: DataGroundClient;
  reference: InvocationReference;
  version: number;
  active: boolean;
  status?: RetentionRead;
  onAccepted?: () => void;
}) {
  const key = `${reference.isolationDomainId}:${reference.invocationId}`;
  const generation = useRef(0);
  const identity = useRef({ client, key });
  identity.current = { client, key };
  const locked = useRef(false);
  const feedback = useRef<HTMLDivElement>(null);
  const [state, setState] = useState<{
    client: DataGroundClient;
    key: string;
    reviewing: boolean;
    pending: boolean;
    attempt?: RetentionRecoveryAttempt;
    result?: RetentionRecoveryResult;
  }>({ client, key, reviewing: false, pending: false });
  useEffect(() => {
    generation.current++;
    locked.current = false;
    setState({ client, key, reviewing: false, pending: false });
    return () => {
      generation.current++;
    };
  }, [client, key]);
  const current = state.client === client && state.key === key;
  const value = status?.kind === "available" ? status.value : undefined;
  const permitted = active && value?.recoveryAccess === "allowed";
  const result = current ? state.result : undefined;
  const pending = current && state.pending;
  const uncertain = result && !result.ok && result.uncertain;
  const eligible = permitted && value?.recovery === "review_required" && version > 0;
  const canStart =
    current &&
    eligible &&
    !pending &&
    !uncertain &&
    (!result?.ok || version > result.receipt.invocationVersion);
  useEffect(() => {
    if (active && result) feedback.current?.focus();
  }, [active, result]);
  function review() {
    if (!canStart || locked.current) return;
    try {
      const attempt = {
        expectedVersion: version,
        idempotencyKey: `retention-recovery:${globalThis.crypto.randomUUID().replaceAll("-", "")}`,
      };
      setState({ client, key, reviewing: true, pending: false, attempt });
    } catch {
      setState({
        client,
        key,
        reviewing: false,
        pending: false,
        result: {
          ok: false,
          uncertain: false,
          message: "A stable recovery request could not be prepared. Refresh before trying again.",
        },
      });
    }
  }
  async function submit(replay: boolean) {
    if (
      !current ||
      locked.current ||
      pending ||
      !permitted ||
      !state.attempt ||
      (replay
        ? !uncertain
        : !state.reviewing || !eligible || state.attempt.expectedVersion !== version)
    )
      return;
    locked.current = true;
    const request = generation.current,
      attempt = state.attempt;
    setState({ ...state, pending: true });
    try {
      const response = await requestWorkspaceRetentionRecovery(client, reference, attempt);
      if (
        generation.current !== request ||
        identity.current.client !== client ||
        identity.current.key !== key
      )
        return;
      // A denied retry says nothing about the original uncertain submission.
      // Keep its exact request until acceptance is observed or identity changes.
      const result =
        replay && !response.ok && !response.uncertain
          ? {
              ...response,
              uncertain: true,
              message:
                "The retry was rejected. The earlier recovery request outcome is still unknown. Refresh access and run state before retrying the exact request.",
            }
          : response;
      setState({ client, key, reviewing: false, pending: false, attempt, result });
      if (result.ok) onAccepted?.();
    } finally {
      if (generation.current === request) locked.current = false;
    }
  }
  if (
    !current ||
    !active ||
    (!canStart &&
      !state.reviewing &&
      !pending &&
      !result &&
      value?.recoveryAccess !== "unavailable")
  )
    return null;
  return (
    <>
      {canStart && <Button onPress={review}>Request retention recovery</Button>}
      {value?.recovery === "review_required" && value.recoveryAccess === "unavailable" && (
        <p>
          Recovery permission could not be checked. Refresh retention status before requesting
          recovery.
        </p>
      )}
      {result && (
        <div role={result.ok ? "status" : "alert"} ref={feedback} tabIndex={-1}>
          {result.ok ? (
            <p>
              Recovery request accepted. Its retention window ends{" "}
              <time dateTime={result.receipt.deadlineAt}>
                {new Date(result.receipt.deadlineAt).toLocaleString()}
              </time>
              . Refresh the run to check completion.
            </p>
          ) : (
            <p>{result.message}</p>
          )}
        </div>
      )}
      {uncertain && (
        <Button isDisabled={!permitted || pending} onPress={() => void submit(true)}>
          Retry exact recovery request
        </Button>
      )}
      {pending && <p role="status">Submitting retention recovery…</p>}
      <Dialog
        title="Recover workspace changes?"
        isOpen={active && current && state.reviewing}
        isDismissDisabled={pending}
        onClose={() => {
          if (!pending) setState({ client, key, reviewing: false, pending: false });
        }}
      >
        <p>
          Ask the worker to retain final changes from this stopped workspace and finish
          cancellation. The worker must verify your current access and the retained data.
        </p>
        <p>
          Scope: <code>{reference.isolationDomainId}</code>
        </p>
        <details>
          <summary>Run details</summary>
          <code>{reference.invocationId}</code>
        </details>
        {(!eligible || state.attempt?.expectedVersion !== version) && !pending && (
          <p role="alert">
            The reviewed run or your access changed. Close this dialog and refresh before a new
            request.
          </p>
        )}
        <Button
          variant="quiet"
          isDisabled={pending}
          onPress={() => setState({ client, key, reviewing: false, pending: false })}
        >
          Keep unchanged
        </Button>
        <Button
          isDisabled={pending || !eligible || state.attempt?.expectedVersion !== version}
          onPress={() => void submit(false)}
        >
          Request recovery
        </Button>
      </Dialog>
    </>
  );
}
