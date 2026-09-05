import { Button, StatusBadge } from "@dataground/ui";
import { useCallback, useEffect, useId, useReducer, useRef, useState } from "react";
import type { InvocationApprovalReference } from "../approvals";
import type { InvocationArtifactReference } from "../artifacts";
import type { DataGroundClient } from "../contracts/client";
import {
  type AgentServiceInvocationTarget,
  type InvocationSummaryResource,
  listInvocations,
} from "./client";
import { invocationHistoryReducer } from "./history";
import { InvocationInspectionWorkflow } from "./InvocationInspectionWorkflow";

export interface InvocationHistoryWorkflowProps {
  client: DataGroundClient;
  target: AgentServiceInvocationTarget;
}

export function InvocationHistoryWorkflow({ client, target }: InvocationHistoryWorkflowProps) {
  const { isolationDomainId, serviceId } = target;
  const scope = `${isolationDomainId}/${serviceId}`;
  const titleId = useId();
  const inspectionHeading = useRef<HTMLHeadingElement>(null);
  const returnFocusId = useRef<string | undefined>(undefined);
  const sequence = useRef(0);
  const [state, dispatch] = useReducer(invocationHistoryReducer, {
    client,
    scope,
    requestId: 0,
    loading: true,
    items: [],
    seenCursors: [],
  });
  const [selected, setSelected] = useState<{
    client: DataGroundClient;
    invocation: InvocationSummaryResource;
  }>();
  const [approval, setApproval] = useState<InvocationApprovalReference>();
  const [artifact, setArtifact] = useState<InvocationArtifactReference>();
  const load = useCallback(
    async (cursor?: string) => {
      const requestId = ++sequence.current;
      if (cursor === undefined) {
        setSelected(undefined);
        setApproval(undefined);
        setArtifact(undefined);
      }
      dispatch({ type: "requested", client, scope, requestId, cursor });
      const result = await listInvocations(client, { isolationDomainId, serviceId }, cursor);
      if (sequence.current === requestId) dispatch({ type: "received", requestId, result });
    },
    [client, scope, isolationDomainId, serviceId],
  );
  useEffect(() => {
    void load();
    return () => {
      sequence.current++;
    };
  }, [load]);

  // Hide old state synchronously when the connection or service changes, before
  // the effect resets it; late requests cannot restore another scope's history.
  const current = state.client === client && state.scope === scope;
  const invocation =
    current &&
    selected?.client === client &&
    selected.invocation.metadata.isolationDomainId === isolationDomainId &&
    selected.invocation.serviceId === serviceId
      ? selected.invocation
      : undefined;
  useEffect(() => {
    if (invocation) {
      inspectionHeading.current?.focus();
    } else if (returnFocusId.current) {
      document.getElementById(returnFocusId.current)?.focus();
      returnFocusId.current = undefined;
    }
  }, [invocation]);
  if (invocation) {
    return (
      <section aria-labelledby={titleId} className="invocation-history">
        <div className="workbench-page-heading workbench-page-heading--compact">
          <h2 id={titleId} ref={inspectionHeading} tabIndex={-1}>
            Invocation {invocation.metadata.id}
          </h2>
          <Button
            variant="quiet"
            onPress={() => {
              returnFocusId.current = `${titleId}-open-${invocation.metadata.id}`;
              setSelected(undefined);
              setApproval(undefined);
              setArtifact(undefined);
            }}
          >
            Back to invocation history
          </Button>
        </div>
        <InvocationInspectionWorkflow
          key={invocation.metadata.id}
          client={client}
          canCancelInvocation
          canResolveApproval
          reference={{ isolationDomainId, invocationId: invocation.metadata.id }}
          selectedApproval={approval}
          selectedArtifact={artifact}
          onCloseApproval={() => setApproval(undefined)}
          onCloseArtifact={() => setArtifact(undefined)}
          onInspectApproval={(reference) => {
            setArtifact(undefined);
            setApproval(reference);
          }}
          onInspectArtifact={(reference) => {
            setApproval(undefined);
            setArtifact(reference);
          }}
        />
      </section>
    );
  }
  const items = current ? state.items : [];
  const loading = !current || state.loading;
  return (
    <section aria-labelledby={titleId} className="invocation-history">
      <div className="workbench-page-heading workbench-page-heading--compact">
        <div>
          <h2 id={titleId}>Invocation history</h2>
          <p>Earlier runs of this service, newest first. Open a run to read its current state.</p>
        </div>
        <Button
          isDisabled={loading}
          onPress={() => {
            void load();
          }}
          variant="quiet"
        >
          Refresh invocation history
        </Button>
      </div>
      {current && state.error && (
        <div role="alert" className="workbench-inline-error">
          <p>{state.error.message}</p>
          {state.error.retryable && (
            <Button
              isDisabled={loading}
              onPress={() => {
                void load(state.requestCursor);
              }}
              variant="quiet"
            >
              Retry invocation history
            </Button>
          )}
        </div>
      )}
      {loading && <p role="status">Loading invocation history…</p>}
      {!loading && !state.error && items.length === 0 && (
        <p>No invocations yet for this service.</p>
      )}
      {items.length > 0 && (
        <ol className="invocation-history-list">
          {items.map((item) => (
            <li key={item.metadata.id}>
              <div>
                <StatusBadge
                  tone={
                    item.state === "succeeded"
                      ? "success"
                      : item.state === "failed"
                        ? "critical"
                        : item.state === "unknown"
                          ? "warning"
                          : "neutral"
                  }
                >
                  {item.state}
                </StatusBadge>{" "}
                <time dateTime={item.metadata.createdAt}>{item.metadata.createdAt}</time>
              </div>
              <p>
                Route {item.alias} · Revision <code>{item.revisionId}</code>
              </p>
              <Button
                id={`${titleId}-open-${item.metadata.id}`}
                variant="quiet"
                onPress={() => {
                  setApproval(undefined);
                  setArtifact(undefined);
                  setSelected({ client, invocation: item });
                }}
              >
                Open invocation {item.metadata.id}
              </Button>
            </li>
          ))}
        </ol>
      )}
      {current && state.nextCursor && !state.error && (
        <Button
          isDisabled={loading}
          onPress={() => {
            void load(state.nextCursor);
          }}
          variant="quiet"
        >
          Load more invocations
        </Button>
      )}
    </section>
  );
}
