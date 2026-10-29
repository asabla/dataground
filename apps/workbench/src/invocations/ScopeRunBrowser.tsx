import {
  Button,
  DataTable,
  type DataTableSort,
  SelectField,
  StatusBadge,
  TextField,
} from "@dataground/ui";
import { useCallback, useEffect, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import {
  type InvocationFailure,
  listScopeInvocations,
  type ScopeInvocationSummary,
  type ScopeRunState,
  scopeRunStates,
} from "./client";

interface RunPage {
  client: DataGroundClient;
  scope: string;
  filter: string;
  loading: boolean;
  items: ScopeInvocationSummary[];
  cursors: string[];
  nextCursor?: string;
  error?: InvocationFailure;
}
export function ScopeRunBrowser({
  client,
  isolationDomainId,
  onOpen,
}: {
  client: DataGroundClient;
  isolationDomainId: string;
  onOpen: (run: ScopeInvocationSummary) => void;
}) {
  const [filter, setFilter] = useState<ScopeRunState | "all">("all");
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<DataTableSort>({ column: "created", direction: "descending" });
  const generation = useRef(0);
  const busy = useRef(false);
  const identity = useRef({ client, isolationDomainId, filter });
  identity.current = { client, isolationDomainId, filter };
  const [page, setPage] = useState<RunPage>({
    client,
    scope: isolationDomainId,
    filter,
    loading: true,
    items: [],
    cursors: [],
  });
  const latest = useRef(page);
  latest.current = page;
  const load = useCallback(
    async (cursor?: string) => {
      if (
        busy.current ||
        identity.current.client !== client ||
        identity.current.isolationDomainId !== isolationDomainId ||
        identity.current.filter !== filter
      )
        return;
      const request = ++generation.current;
      busy.current = true;
      setPage((previous) => ({
        client,
        scope: isolationDomainId,
        filter,
        loading: true,
        items: cursor ? previous.items : [],
        cursors: cursor ? previous.cursors : [],
      }));
      const result = await listScopeInvocations(
        client,
        isolationDomainId,
        filter === "all" ? undefined : filter,
        cursor,
      );
      if (
        request !== generation.current ||
        identity.current.client !== client ||
        identity.current.isolationDomainId !== isolationDomainId ||
        identity.current.filter !== filter
      )
        return;
      busy.current = false;
      setPage((previous) => {
        if (!result.ok)
          return { ...previous, loading: false, items: [], cursors: [], error: result.error };
        const { items, nextCursor } = result.page;
        if (
          (nextCursor && previous.cursors.includes(nextCursor)) ||
          items.some((item) => previous.items.some((old) => old.metadata.id === item.metadata.id))
        )
          return {
            ...previous,
            loading: false,
            items: [],
            cursors: [],
            error: {
              code: "WORKBENCH_RUN_DISCOVERY_STALLED",
              message: "Run discovery did not advance. Refresh runs to recover.",
              retryable: false,
            },
          };
        return {
          ...previous,
          loading: false,
          items: [...previous.items, ...items],
          nextCursor,
          cursors: nextCursor ? [...previous.cursors, nextCursor] : previous.cursors,
        };
      });
    },
    [client, isolationDomainId, filter],
  );
  useEffect(() => {
    busy.current = false;
    void load();
    return () => {
      generation.current++;
      busy.current = false;
    };
  }, [load]);
  const current =
    page.client === client && page.scope === isolationDomainId && page.filter === filter;
  const loading = !current || page.loading;
  const items = current ? page.items : [];
  const visible = items
    .filter((item) =>
      `${item.serviceName} ${item.alias}`.toLocaleLowerCase().includes(query.toLocaleLowerCase()),
    )
    .sort(
      (a, b) =>
        (sort.direction === "descending" ? -1 : 1) *
        (sort.column === "service"
          ? a.serviceName.localeCompare(b.serviceName)
          : Date.parse(a.metadata.createdAt) - Date.parse(b.metadata.createdAt)),
    );
  const open = (item: ScopeInvocationSummary) => {
    if (
      latest.current === page &&
      current &&
      !loading &&
      !busy.current &&
      !page.error &&
      identity.current.client === client &&
      identity.current.isolationDomainId === isolationDomainId &&
      identity.current.filter === filter
    )
      onOpen(item);
  };
  return (
    <section aria-labelledby="scope-runs-title">
      <div className="workbench-page-heading">
        <div>
          <p className="workbench-kicker">Agents</p>
          <h1 id="scope-runs-title" tabIndex={-1}>
            Runs
          </h1>
          <p>
            Find executions across services in this scope. Opening a run requires separate access.
          </p>
        </div>
        <Button variant="quiet" isDisabled={loading} onPress={() => void load()}>
          Refresh runs
        </Button>
      </div>
      <div className="resource-browser__toolbar">
        <TextField
          label="Search loaded services and routes"
          type="search"
          value={query}
          onChange={setQuery}
        />
        <SelectField
          label="Run state"
          value={filter}
          onChange={(value) => {
            if (value === "all" || scopeRunStates.includes(value as ScopeRunState))
              setFilter(value as ScopeRunState | "all");
          }}
          options={[
            { value: "all", label: "All states" },
            ...scopeRunStates.map((value) => ({ value, label: value })),
          ]}
        />
        <p>
          {visible.length} of {items.length} loaded runs shown.
        </p>
      </div>
      {loading && <p role="status">Loading runs…</p>}
      {current && page.error && (
        <p role="alert" className="workbench-inline-error">
          {page.error.message}
        </p>
      )}
      {!loading && !page.error && (
        <DataTable
          label="Runs across services"
          items={visible}
          getRowId={(item) => item.metadata.id}
          getRowLabel={(item) => `${item.serviceName}, ${item.alias}, ${item.state}`}
          sort={sort}
          onSortChange={setSort}
          emptyMessage={
            items.length
              ? "No loaded runs match this search."
              : "No runs match this state in the current scope."
          }
          columns={[
            {
              id: "service",
              title: "Service",
              isRowHeader: true,
              allowsSorting: true,
              render: (item) => <strong>{item.serviceName}</strong>,
            },
            { id: "route", title: "Route", render: (item) => item.alias },
            {
              id: "state",
              title: "State",
              render: (item) => (
                <StatusBadge
                  tone={
                    item.state === "failed"
                      ? "critical"
                      : ["waiting", "unknown", "cancelling"].includes(item.state)
                        ? "warning"
                        : "neutral"
                  }
                >
                  {item.state}
                </StatusBadge>
              ),
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
              id: "completed",
              title: "Completed",
              render: (item) =>
                item.completedAt ? (
                  <time dateTime={item.completedAt}>
                    {new Date(item.completedAt).toLocaleString()}
                  </time>
                ) : (
                  "Not recorded"
                ),
            },
            {
              id: "actions",
              title: "Actions",
              render: (item) => (
                <Button
                  variant="quiet"
                  aria-label={`Inspect ${item.serviceName}, ${item.alias}, ${item.state}`}
                  onPress={() => open(item)}
                >
                  Inspect
                </Button>
              ),
            },
          ]}
        />
      )}
      {current && page.nextCursor && !page.error && (
        <Button
          variant="quiet"
          isDisabled={loading}
          onPress={() => {
            if (latest.current === page) void load(page.nextCursor);
          }}
        >
          Load more runs
        </Button>
      )}
    </section>
  );
}
