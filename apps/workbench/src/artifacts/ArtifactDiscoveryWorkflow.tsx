import {
  Button,
  DataTable,
  type DataTableSort,
  SelectField,
  StatusBadge,
  TextField,
} from "@dataground/ui";
import { useCallback, useEffect, useId, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import {
  type ArtifactFailure,
  type InvocationArtifact,
  type InvocationArtifactListReference,
  type InvocationArtifactReference,
  listInvocationArtifacts,
} from "./client";

interface DiscoveryState {
  client: DataGroundClient;
  scope: string;
  loading: boolean;
  items: InvocationArtifact[];
  seenCursors: string[];
  nextCursor?: string;
  error?: ArtifactFailure;
}

export function ArtifactDiscoveryWorkflow({
  client,
  reference,
  onInspectArtifact,
}: {
  client: DataGroundClient;
  reference: InvocationArtifactListReference;
  onInspectArtifact?: (reference: InvocationArtifactReference) => void;
}) {
  const { isolationDomainId, invocationId } = reference;
  const scope = `${isolationDomainId}/${invocationId}`;
  const titleId = useId();
  const [stateFilter, setStateFilter] = useState("all");
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<DataTableSort>({ column: "created", direction: "descending" });
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
      const result = await listInvocationArtifacts(
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
          items.some((item) =>
            previous.items.some((existing) => existing.metadata.id === item.metadata.id),
          )
        ) {
          return {
            ...previous,
            loading: false,
            items: [],
            seenCursors: [],
            error: {
              code: "WORKBENCH_ARTIFACT_DISCOVERY_STALLED",
              message: "Artifact discovery did not advance. Refresh artifacts to recover.",
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
  const inspect = (item: InvocationArtifact) => {
    if (
      currentState.current === state &&
      !state.error &&
      currentIdentity.current.client === client &&
      currentIdentity.current.scope === scope &&
      activeRequest.current === undefined
    )
      onInspectArtifact?.({ isolationDomainId, invocationId, artifactId: item.metadata.id });
  };
  const visibleItems = items
    .filter(
      (item) =>
        (stateFilter === "all" || item.state === stateFilter) &&
        item.name.toLocaleLowerCase().includes(query.toLocaleLowerCase()),
    )
    .sort(
      (left, right) =>
        (sort.direction === "descending" ? -1 : 1) *
        (sort.column === "name"
          ? left.name.localeCompare(right.name)
          : Date.parse(left.metadata.createdAt) - Date.parse(right.metadata.createdAt)),
    );
  return (
    <section aria-labelledby={titleId} className="invocation-history">
      <div className="workbench-page-heading workbench-page-heading--compact">
        <div>
          <h2 id={titleId}>Files and artifacts</h2>
          <p>Outputs from this run. Open metadata before requesting file content.</p>
        </div>
        <Button variant="quiet" isDisabled={loading} onPress={() => void load()}>
          Refresh artifacts
        </Button>
      </div>
      {loading && <p role="status">Loading artifacts…</p>}
      {current && state.error && (
        <div role="alert" className="workbench-inline-error">
          <p>{state.error.message}</p>
        </div>
      )}
      {!loading && !state.error && items.length === 0 && <p>No artifacts recorded for this run.</p>}
      {items.length > 0 && (
        <>
          <div className="resource-browser__toolbar">
            <TextField
              label="Search loaded artifacts"
              type="search"
              value={query}
              onChange={setQuery}
            />
            <SelectField
              label="Artifact state"
              value={stateFilter}
              onChange={setStateFilter}
              options={[
                { value: "all", label: "All states" },
                ...["pending", "available", "failed", "deleted"].map((value) => ({
                  value,
                  label: value,
                })),
              ]}
            />
            <p>
              {visibleItems.length} of {items.length} loaded artifacts shown.
            </p>
          </div>
          <DataTable
            label="Loaded run artifacts"
            items={visibleItems}
            getRowId={(item) => item.metadata.id}
            getRowLabel={(item) => item.name}
            sort={sort}
            onSortChange={setSort}
            isLoading={loading}
            emptyMessage="No loaded artifacts match these filters."
            columns={[
              {
                id: "name",
                title: "Name",
                isRowHeader: true,
                allowsSorting: true,
                render: (item) => <strong>{item.name}</strong>,
              },
              {
                id: "state",
                title: "State",
                render: (item) => (
                  <StatusBadge
                    tone={
                      item.state === "failed"
                        ? "critical"
                        : item.state === "pending"
                          ? "warning"
                          : "neutral"
                    }
                  >
                    {item.state}
                  </StatusBadge>
                ),
              },
              { id: "type", title: "Format", render: (item) => item.mediaType },
              {
                id: "size",
                title: "Size",
                render: (item) => `${item.sizeBytes.toLocaleString()} bytes`,
              },
              {
                id: "sensitivity",
                title: "Sensitivity",
                render: (item) => (item.sensitive ? "Sensitive" : "Standard"),
              },
              {
                id: "created",
                title: "Created",
                allowsSorting: true,
                render: (item) => (
                  <time dateTime={item.metadata.createdAt}>
                    {new Date(item.metadata.createdAt).toLocaleString()}
                  </time>
                ),
              },
              {
                id: "actions",
                title: "Actions",
                render: (item) => (
                  <Button
                    variant="quiet"
                    aria-label={`Inspect ${item.name}`}
                    isDisabled={loading || !onInspectArtifact}
                    onPress={() => inspect(item)}
                  >
                    Inspect
                  </Button>
                ),
              },
            ]}
          />
        </>
      )}
      {current && state.nextCursor && !state.error && (
        <Button
          variant="quiet"
          isDisabled={loading}
          onPress={() => {
            if (currentState.current === state) void load(state.nextCursor);
          }}
        >
          Load more artifacts
        </Button>
      )}
    </section>
  );
}
