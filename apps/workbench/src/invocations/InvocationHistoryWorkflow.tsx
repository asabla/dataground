import { Button, DataTable, type DataTableSort, SelectField, StatusBadge } from "@dataground/ui";
import { useCallback, useEffect, useId, useReducer, useRef, useState } from "react";
import type { InvocationApprovalReference } from "../approvals";
import type { InvocationArtifactReference } from "../artifacts";
import type { DataGroundClient } from "../contracts/client";
import type { InvocationQuestionReference } from "../questions";
import {
  type AgentServiceInvocationTarget,
  type InvocationSummaryResource,
  listInvocations,
} from "./client";
import { invocationHistoryReducer } from "./history";
import { InvocationInspectionWorkflow } from "./InvocationInspectionWorkflow";

export interface InvocationHistoryWorkflowProps {
  presentation?: "classic" | "workbench";
  onInspectionChange?: (inspecting: boolean) => void;
  client: DataGroundClient;
  target: AgentServiceInvocationTarget;
}

export function InvocationHistoryWorkflow(props: InvocationHistoryWorkflowProps) {
  const scope = `${props.target.isolationDomainId}/${props.target.serviceId}`;
  const [identity, setIdentity] = useState({ client: props.client, scope, generation: 0 });
  if (identity.client !== props.client || identity.scope !== scope) {
    setIdentity({ client: props.client, scope, generation: identity.generation + 1 });
    return null;
  }
  return <InvocationHistorySession key={identity.generation} {...props} />;
}

function InvocationHistorySession({
  client,
  target,
  presentation = "classic",
  onInspectionChange,
}: InvocationHistoryWorkflowProps) {
  const [sort, setSort] = useState<DataTableSort>({ column: "created", direction: "descending" });
  const [stateFilter, setStateFilter] = useState("all");
  const { isolationDomainId, serviceId } = target;
  const scope = `${isolationDomainId}/${serviceId}`;
  const titleId = useId();
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
  const [opened, setOpened] = useState<InvocationSummaryResource[]>([]);
  const load = useCallback(
    async (cursor?: string) => {
      const requestId = ++sequence.current;
      if (cursor === undefined) {
        setSelected(undefined);
      }
      dispatch({ type: "requested", client, scope, requestId, cursor });
      const result = await listInvocations(client, { isolationDomainId, serviceId }, cursor);
      if (sequence.current !== requestId) return;
      if (!result.ok && [401, 403].includes(result.error.status ?? 0)) {
        setOpened([]);
        setSelected(undefined);
      }
      dispatch({ type: "received", requestId, result });
    },
    [client, scope, isolationDomainId, serviceId],
  );
  useEffect(() => {
    setStateFilter("all");
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
    if (!invocation && returnFocusId.current) {
      document.getElementById(returnFocusId.current)?.focus();
      returnFocusId.current = undefined;
    }
  }, [invocation]);
  useEffect(() => {
    onInspectionChange?.(invocation !== undefined);
    return () => onInspectionChange?.(false);
  }, [invocation, onInspectionChange]);
  const items = current ? state.items : [];
  const loading = !current || state.loading;
  return (
    <>
      {opened.map((item) => {
        const active = invocation?.metadata.id === item.metadata.id;
        return (
          <div key={item.metadata.id} hidden={!active} inert={!active}>
            <RunInspectionSession
              client={client}
              invocation={item}
              presentation={presentation}
              active={active}
              onBack={() => {
                returnFocusId.current = `${titleId}-open-${item.metadata.id}`;
                setSelected(undefined);
                if (presentation === "classic") setOpened([]);
              }}
            />
          </div>
        );
      })}
      <section
        aria-labelledby={titleId}
        className="invocation-history"
        hidden={!!invocation}
        inert={!!invocation}
      >
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
          <>
            <div className="resource-browser__toolbar">
              <SelectField
                label="Run state"
                value={stateFilter}
                onChange={setStateFilter}
                options={[
                  { value: "all", label: "All states" },
                  ...Array.from(
                    new Set([
                      ...items.map((item) => item.state),
                      ...(stateFilter === "all" ? [] : [stateFilter]),
                    ]),
                  )
                    .sort()
                    .map((value) => ({ value, label: value })),
                ]}
              />
              <p>
                {items.filter((item) => stateFilter === "all" || item.state === stateFilter).length}{" "}
                of {items.length} loaded runs shown.
              </p>
            </div>
            <DataTable
              sort={sort}
              onSortChange={setSort}
              label="Loaded service runs"
              items={items
                .filter((item) => stateFilter === "all" || item.state === stateFilter)
                .sort(
                  (a, b) =>
                    (sort.direction === "ascending" ? 1 : -1) *
                    (Date.parse(a.metadata.createdAt) - Date.parse(b.metadata.createdAt) ||
                      a.metadata.id.localeCompare(b.metadata.id)),
                )}
              getRowId={(item) => item.metadata.id}
              getRowLabel={(item) => `Run created ${item.metadata.createdAt}, ${item.state}`}
              isLoading={loading}
              emptyMessage="No loaded runs match this state."
              columns={[
                {
                  id: "created",
                  title: "Created",
                  allowsSorting: true,
                  isRowHeader: true,
                  render: (item) => (
                    <time dateTime={item.metadata.createdAt}>
                      {new Date(item.metadata.createdAt).toLocaleString()}
                    </time>
                  ),
                },
                {
                  id: "state",
                  title: "Outcome",
                  render: (item) => (
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
                    </StatusBadge>
                  ),
                },
                { id: "route", title: "Route", render: (item) => item.alias },
                {
                  id: "completed",
                  title: "Completed",
                  render: (item) =>
                    item.completedAt ? (
                      <time dateTime={item.completedAt}>
                        {new Date(item.completedAt).toLocaleString()}
                      </time>
                    ) : (
                      "Not completed"
                    ),
                },
                {
                  id: "actions",
                  title: "Actions",
                  render: (item) => (
                    <Button
                      id={`${titleId}-open-${item.metadata.id}`}
                      aria-label={`Open invocation ${item.metadata.id}`}
                      isDisabled={loading || !!state.error}
                      variant="quiet"
                      onPress={() => {
                        if (!current || loading || state.error) return;
                        setOpened((current) =>
                          current.some((entry) => entry.metadata.id === item.metadata.id)
                            ? current
                            : [...current, item],
                        );
                        setSelected({ client, invocation: item });
                      }}
                    >
                      Open run
                    </Button>
                  ),
                },
              ]}
            />
          </>
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
    </>
  );
}

// The session owns request selection as well as the command forms beneath it.
// Returning to history hides it; only a scope/identity change or denial discards it.
function RunInspectionSession({
  client,
  invocation,
  presentation,
  active,
  onBack,
}: {
  client: DataGroundClient;
  invocation: InvocationSummaryResource;
  presentation: "classic" | "workbench";
  active: boolean;
  onBack: () => void;
}) {
  const titleId = useId();
  const heading = useRef<HTMLHeadingElement>(null);
  const [question, setQuestion] = useState<InvocationQuestionReference>();
  const [approval, setApproval] = useState<InvocationApprovalReference>();
  const [artifact, setArtifact] = useState<InvocationArtifactReference>();
  useEffect(() => {
    if (active) heading.current?.focus();
  }, [active]);
  return (
    <section aria-labelledby={titleId} className="invocation-history">
      <div className="workbench-page-heading workbench-page-heading--compact">
        <h2 id={titleId} ref={heading} tabIndex={-1}>
          {presentation === "workbench" ? "Run details" : `Invocation ${invocation.metadata.id}`}
        </h2>
        <Button variant="quiet" onPress={onBack}>
          Back to invocation history
        </Button>
      </div>
      <InvocationInspectionWorkflow
        presentation={presentation}
        client={client}
        canCancelInvocation
        canAnswerQuestion
        canResolveApproval
        reference={{
          isolationDomainId: invocation.metadata.isolationDomainId,
          invocationId: invocation.metadata.id,
        }}
        selectedQuestion={question}
        onCloseQuestion={() => setQuestion(undefined)}
        selectedApproval={approval}
        selectedArtifact={artifact}
        onCloseApproval={() => setApproval(undefined)}
        onCloseArtifact={() => setArtifact(undefined)}
        onInspectQuestion={(reference) => {
          setApproval(undefined);
          setArtifact(undefined);
          setQuestion(reference);
        }}
        onInspectApproval={(reference) => {
          setQuestion(undefined);
          setArtifact(undefined);
          setApproval(reference);
        }}
        onInspectArtifact={(reference) => {
          setApproval(undefined);
          setQuestion(undefined);
          setArtifact(reference);
        }}
      />
    </section>
  );
}
