import { Button, DataTable, type DataTableSort, SelectField, StatusBadge } from "@dataground/ui";
import { useCallback, useEffect, useId, useReducer, useRef, useState } from "react";
import type { InvocationApprovalReference } from "../approvals";
import type { InvocationArtifactReference } from "../artifacts";
import type { DataGroundClient } from "../contracts/client";
import type { InvocationQuestionReference } from "../questions";
import {
  type AgentServiceInvocationTarget,
  type InvocationFailure,
  type InvocationSummaryResource,
  listInvocations,
  readInvocationStatus,
} from "./client";
import { invocationHistoryReducer } from "./history";
import { InvocationInspectionWorkflow } from "./InvocationInspectionWorkflow";
import type { RunLocation, RunTab } from "./navigation";

export interface InvocationHistoryWorkflowProps {
  active?: boolean;
  refreshGeneration?: number;
  presentation?: "classic" | "workbench";
  location?: RunLocation;
  onNavigate?: (location: RunLocation) => void;
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
  active: sessionActive = true,
  refreshGeneration = 0,
  client,
  target,
  presentation = "classic",
  location,
  onNavigate,
  onInspectionChange,
}: InvocationHistoryWorkflowProps) {
  const [sort, setSort] = useState<DataTableSort>({ column: "created", direction: "descending" });
  const [stateFilter, setStateFilter] = useState("all");
  const { isolationDomainId, serviceId } = target;
  const scope = `${isolationDomainId}/${serviceId}/${refreshGeneration}`;
  const currentScope = useRef(scope);
  currentScope.current = scope;
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
  const openedRef = useRef(opened);
  openedRef.current = opened;
  const [locationRead, setLocationRead] = useState<{
    id: string;
    loading: boolean;
    error?: InvocationFailure;
  }>();
  const locationGeneration = useRef(0);
  const desiredInvocation = location?.invocationId;
  const controlled = location !== undefined;
  const desiredRef = useRef(desiredInvocation);
  desiredRef.current = desiredInvocation;
  const loadLocation = useCallback(() => {
    if (!controlled) return;
    const generation = ++locationGeneration.current;
    setSelected(undefined);
    setLocationRead(undefined);
    if (!desiredInvocation) return;
    const retained = openedRef.current.find((item) => item.metadata.id === desiredInvocation);
    if (retained) {
      setSelected({ client, invocation: retained });
      return;
    }
    setLocationRead({ id: desiredInvocation, loading: true });
    void readInvocationStatus(client, { isolationDomainId, invocationId: desiredInvocation }).then(
      (result) => {
        if (generation !== locationGeneration.current) return;
        if (!result.ok) {
          if ([401, 403].includes(result.error.status ?? 0)) setOpened([]);
          setLocationRead({ id: desiredInvocation, loading: false, error: result.error });
          return;
        }
        if (result.invocation.serviceId !== serviceId) {
          setLocationRead({
            id: desiredInvocation,
            loading: false,
            error: {
              code: "WORKBENCH_INVOCATION_SERVICE_MISMATCH",
              message: "This run does not belong to the selected service.",
              retryable: false,
            },
          });
          return;
        }
        // Retain only the public summary used for navigation. Results and operation
        // contents remain in their separately authorized inspection workflows.
        const {
          metadata,
          serviceId: boundServiceId,
          revisionId,
          operationId,
          correlationId,
          alias,
          state,
          completedAt,
        } = result.invocation;
        const invocation: InvocationSummaryResource = {
          metadata,
          serviceId: boundServiceId,
          revisionId,
          operationId,
          correlationId,
          alias,
          state,
          completedAt,
        };
        setOpened((current) =>
          current.some((item) => item.metadata.id === invocation.metadata.id)
            ? current
            : [...current, invocation],
        );
        setSelected({ client, invocation });
        setLocationRead({ id: desiredInvocation, loading: false });
      },
    );
    return () => {
      locationGeneration.current++;
    };
  }, [client, isolationDomainId, serviceId, controlled, desiredInvocation]);
  useEffect(() => loadLocation(), [loadLocation]);

  const load = useCallback(
    async (cursor?: string) => {
      if (currentScope.current !== scope) return;
      const requestId = ++sequence.current;
      if (cursor === undefined && !controlled) setSelected(undefined);
      dispatch({ type: "requested", client, scope, requestId, cursor });
      const result = await listInvocations(client, { isolationDomainId, serviceId }, cursor);
      if (sequence.current !== requestId || currentScope.current !== scope) return;
      // Exact-run reads have their own authorization. A list denial must not
      // replace an independently authorized direct location with a loading view.
      if (!desiredRef.current && !result.ok && [401, 403].includes(result.error.status ?? 0)) {
        setOpened([]);
        setSelected(undefined);
      }
      dispatch({ type: "received", requestId, result });
    },
    [client, scope, isolationDomainId, serviceId, controlled],
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
    selected?.client === client &&
    selected.invocation.metadata.isolationDomainId === isolationDomainId &&
    selected.invocation.serviceId === serviceId &&
    (!controlled || selected.invocation.metadata.id === desiredInvocation)
      ? selected.invocation
      : undefined;
  useEffect(() => {
    if (!invocation && !desiredInvocation && returnFocusId.current) {
      (document.getElementById(returnFocusId.current) ?? document.getElementById(titleId))?.focus();
      returnFocusId.current = undefined;
    }
  }, [invocation, desiredInvocation, titleId]);
  useEffect(() => {
    onInspectionChange?.(invocation !== undefined || !!desiredInvocation);
    return () => onInspectionChange?.(false);
  }, [invocation, desiredInvocation, onInspectionChange]);
  const items = current ? state.items : [];
  const loading = !current || state.loading;
  return (
    <>
      {controlled && desiredInvocation && !invocation && (
        <section aria-label="Run location" aria-busy={locationRead?.loading || undefined}>
          <h2>
            {locationRead?.id === desiredInvocation && locationRead.error
              ? "Run unavailable"
              : "Loading run"}
          </h2>
          {locationRead?.id === desiredInvocation && locationRead.error && (
            <div role="alert">
              <p>{locationRead.error.message}</p>
              <Button
                onPress={() => {
                  loadLocation();
                }}
              >
                Retry run read
              </Button>
            </div>
          )}
          <Button
            variant="quiet"
            onPress={() => {
              returnFocusId.current = titleId;
              onNavigate?.({});
            }}
          >
            Back to invocation history
          </Button>
        </section>
      )}
      {opened.map((item) => {
        const active = sessionActive && invocation?.metadata.id === item.metadata.id;
        return (
          <div key={item.metadata.id} hidden={!active} inert={!active}>
            <RunInspectionSession
              client={client}
              invocation={item}
              presentation={presentation}
              active={active}
              selectedTab={active && controlled ? (location?.tab ?? "activity") : undefined}
              artifactLocation={
                active && controlled ? { artifactId: location?.artifactId } : undefined
              }
              onArtifactNavigate={
                controlled
                  ? (artifactId) =>
                      onNavigate?.({
                        invocationId: item.metadata.id,
                        tab: location?.tab,
                        artifactId,
                      })
                  : undefined
              }
              onSelectTab={
                controlled
                  ? (tab) =>
                      onNavigate?.({
                        invocationId: item.metadata.id,
                        tab: tab === "activity" ? undefined : tab,
                        artifactId: location?.artifactId,
                      })
                  : undefined
              }
              onBack={() => {
                returnFocusId.current = `${titleId}-open-${item.metadata.id}`;
                setSelected(undefined);
                if (controlled) onNavigate?.({});
                if (presentation === "classic") setOpened([]);
              }}
            />
          </div>
        );
      })}
      <section
        aria-labelledby={titleId}
        className="invocation-history"
        hidden={!!invocation || !!desiredInvocation}
        inert={!!invocation || !!desiredInvocation}
      >
        <div className="workbench-page-heading workbench-page-heading--compact">
          <div>
            <h2 id={titleId} tabIndex={-1}>
              Invocation history
            </h2>
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
                        if (!current || currentScope.current !== scope || loading || state.error)
                          return;
                        setOpened((current) =>
                          current.some((entry) => entry.metadata.id === item.metadata.id)
                            ? current
                            : [...current, item],
                        );
                        if (controlled) onNavigate?.({ invocationId: item.metadata.id });
                        else setSelected({ client, invocation: item });
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
  selectedTab,
  onSelectTab,
  artifactLocation,
  onArtifactNavigate,
  onBack,
}: {
  client: DataGroundClient;
  invocation: InvocationSummaryResource;
  presentation: "classic" | "workbench";
  active: boolean;
  selectedTab?: RunTab;
  onSelectTab?: (tab: RunTab) => void;
  artifactLocation?: { artifactId?: string };
  onArtifactNavigate?: (artifactId?: string) => void;
  onBack: () => void;
}) {
  const titleId = useId();
  const heading = useRef<HTMLHeadingElement>(null);
  const [question, setQuestion] = useState<InvocationQuestionReference>();
  const [approval, setApproval] = useState<InvocationApprovalReference>();
  const [artifact, setArtifact] = useState<InvocationArtifactReference>();
  const selectedArtifact =
    artifactLocation === undefined
      ? artifact
      : artifactLocation.artifactId
        ? {
            artifactId: artifactLocation.artifactId,
            invocationId: invocation.metadata.id,
            isolationDomainId: invocation.metadata.isolationDomainId,
          }
        : undefined;
  const artifactControlled = artifactLocation !== undefined;
  const locationArtifactId = artifactLocation?.artifactId;
  useEffect(() => {
    if (!artifactControlled) return;
    setArtifact(
      locationArtifactId
        ? {
            artifactId: locationArtifactId,
            invocationId: invocation.metadata.id,
            isolationDomainId: invocation.metadata.isolationDomainId,
          }
        : undefined,
    );
    if (locationArtifactId) {
      setQuestion(undefined);
      setApproval(undefined);
    }
  }, [
    artifactControlled,
    locationArtifactId,
    invocation.metadata.id,
    invocation.metadata.isolationDomainId,
  ]);
  const clearArtifact = () => {
    setArtifact(undefined);
    if (selectedArtifact) onArtifactNavigate?.(undefined);
  };
  useEffect(() => {
    if (!active) return;
    const inspector = heading.current
      ?.closest("section")
      ?.querySelector<HTMLElement>('[aria-label="Selected interaction item"]');
    (inspector ?? heading.current)?.focus();
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
        active={active}
        selectedTab={selectedTab}
        onSelectTab={onSelectTab}
        presentation={presentation}
        client={client}
        canCancelInvocation
        canAnswerQuestion
        canResolveApproval
        reference={{
          isolationDomainId: invocation.metadata.isolationDomainId,
          invocationId: invocation.metadata.id,
        }}
        selectedQuestion={artifactLocation?.artifactId ? undefined : question}
        onCloseQuestion={() => setQuestion(undefined)}
        selectedApproval={artifactLocation?.artifactId ? undefined : approval}
        selectedArtifact={selectedArtifact}
        onCloseApproval={() => setApproval(undefined)}
        onCloseArtifact={clearArtifact}
        onInspectQuestion={(reference) => {
          setApproval(undefined);
          clearArtifact();
          setQuestion(reference);
        }}
        onInspectApproval={(reference) => {
          setQuestion(undefined);
          clearArtifact();
          setApproval(reference);
        }}
        onInspectArtifact={(reference) => {
          if (
            reference.invocationId !== invocation.metadata.id ||
            reference.isolationDomainId !== invocation.metadata.isolationDomainId
          )
            return;
          setApproval(undefined);
          setQuestion(undefined);
          setArtifact(reference);
          onArtifactNavigate?.(reference.artifactId);
        }}
      />
    </section>
  );
}
