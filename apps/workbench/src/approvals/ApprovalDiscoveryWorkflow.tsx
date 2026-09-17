import { Button, StatusBadge } from "@dataground/ui";
import { useCallback, useEffect, useId, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import {
  type ApprovalFailure,
  type InvocationApproval,
  type InvocationApprovalListReference,
  type InvocationApprovalReference,
  listInvocationApprovals,
} from "./client";

interface DiscoveryState {
  client: DataGroundClient;
  scope: string;
  loading: boolean;
  items: InvocationApproval[];
  seenCursors: string[];
  nextCursor?: string;
  error?: ApprovalFailure;
}

export function ApprovalDiscoveryWorkflow({
  client,
  reference,
  onInspectApproval,
}: {
  client: DataGroundClient;
  reference: InvocationApprovalListReference;
  onInspectApproval: (reference: InvocationApprovalReference) => void;
}) {
  const { isolationDomainId, invocationId } = reference;
  const scope = `${isolationDomainId}/${invocationId}`;
  const titleId = useId();
  const sequence = useRef(0);
  const activeRequest = useRef<number | undefined>(undefined);
  const currentIdentity = useRef({ client, scope });
  currentIdentity.current = { client, scope };
  const [state, setState] = useState<DiscoveryState>({
    client,
    scope,
    loading: true,
    items: [],
    seenCursors: [],
  });
  const currentState = useRef(state);
  currentState.current = state;
  const load = useCallback(
    async (cursor?: string) => {
      if (
        currentIdentity.current.client !== client ||
        currentIdentity.current.scope !== scope ||
        activeRequest.current !== undefined
      )
        return;
      const requestId = ++sequence.current;
      activeRequest.current = requestId;
      setState((previous) => ({
        client,
        scope,
        loading: true,
        items: cursor === undefined ? [] : previous.items,
        seenCursors: cursor === undefined ? [] : previous.seenCursors,
      }));
      const result = await listInvocationApprovals(
        client,
        { isolationDomainId, invocationId },
        cursor,
      );
      if (
        sequence.current !== requestId ||
        currentIdentity.current.client !== client ||
        currentIdentity.current.scope !== scope
      )
        return;
      activeRequest.current = undefined;
      setState((previous) => {
        if (!result.ok)
          return { ...previous, loading: false, items: [], seenCursors: [], error: result.error };
        const { items, nextCursor } = result.page;
        if (
          (nextCursor !== undefined && previous.seenCursors.includes(nextCursor)) ||
          items.some((item) => previous.items.some((existing) => existing.id === item.id))
        ) {
          return {
            ...previous,
            loading: false,
            items: [],
            seenCursors: [],
            error: {
              code: "WORKBENCH_APPROVAL_DISCOVERY_STALLED",
              message: "Approval discovery did not advance. Refresh approvals to recover.",
              retryable: false,
            },
          };
        }
        return {
          ...previous,
          loading: false,
          items: [...previous.items, ...items],
          nextCursor,
          seenCursors:
            nextCursor === undefined ? previous.seenCursors : [...previous.seenCursors, nextCursor],
        };
      });
    },
    [client, scope, isolationDomainId, invocationId],
  );
  useEffect(() => {
    activeRequest.current = undefined;
    void load();
    return () => {
      sequence.current++;
      activeRequest.current = undefined;
    };
  }, [load]);
  const current = state.client === client && state.scope === scope;
  const loading = !current || state.loading;
  const items = current ? state.items : [];
  return (
    <section aria-labelledby={titleId} className="invocation-history">
      <div className="workbench-page-heading workbench-page-heading--compact">
        <div>
          <h2 id={titleId}>Invocation approvals</h2>
          <p>
            Retained requests, newest first. Refresh to find new requests and current states. Open a
            request to read its latest state before making a decision.
          </p>
        </div>
        <Button
          variant="quiet"
          isDisabled={loading}
          onPress={() => {
            void load();
          }}
        >
          Refresh approvals
        </Button>
      </div>
      {loading && <p role="status">Loading approvals…</p>}
      {current && state.error && (
        <div role="alert" className="workbench-inline-error">
          <p>{state.error.message}</p>
        </div>
      )}
      {!loading && !state.error && items.length === 0 && (
        <p>No retained approval requests for this invocation.</p>
      )}
      {items.length > 0 && (
        <ol className="invocation-history-list">
          {items.map((item) => (
            <li key={item.id}>
              <StatusBadge tone={item.state === "delivery_unknown" ? "warning" : "neutral"}>
                {item.state.replaceAll("_", " ")}
              </StatusBadge>
              <p>
                {item.requestedAction === "process.execute"
                  ? "Run a process"
                  : "Change workspace files"}{" "}
                · <time dateTime={item.createdAt}>{item.createdAt}</time>
              </p>
              <Button
                variant="quiet"
                isDisabled={loading}
                onPress={() => {
                  if (
                    currentState.current === state &&
                    !state.error &&
                    currentIdentity.current.client === client &&
                    currentIdentity.current.scope === scope &&
                    activeRequest.current === undefined
                  )
                    onInspectApproval({ isolationDomainId, invocationId, approvalId: item.id });
                }}
              >
                Open approval {item.id}
              </Button>
            </li>
          ))}
        </ol>
      )}
      {current && state.nextCursor && !state.error && (
        <Button
          variant="quiet"
          isDisabled={loading}
          onPress={() => {
            if (currentState.current === state) void load(state.nextCursor);
          }}
        >
          Load more approvals
        </Button>
      )}
    </section>
  );
}
