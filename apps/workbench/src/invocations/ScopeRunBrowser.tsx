import {
  Button,
  DataTable,
  type DataTableSort,
  SelectField,
  StatusBadge,
  TextField,
} from "@dataground/ui";
import { useCallback, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import { useDiscoveryPage } from "../discovery/useDiscoveryPage";
import {
  listScopeInvocations,
  type ScopeInvocationSummary,
  type ScopeRunState,
  scopeRunStates,
} from "./client";

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
  const read = useCallback(
    (cursor?: string) =>
      listScopeInvocations(
        client,
        isolationDomainId,
        filter === "all" ? undefined : filter,
        cursor,
      ),
    [client, isolationDomainId, filter],
  );
  const { items, loading, error, nextCursor, load, canAct } = useDiscoveryPage(
    read,
    "Run discovery did not advance. Refresh runs to recover.",
  );
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
    if (canAct()) onOpen(item);
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
      {error && (
        <p role="alert" className="workbench-inline-error">
          {error.message}
        </p>
      )}
      {!loading && !error && (
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
      {nextCursor && !error && (
        <Button
          variant="quiet"
          isDisabled={loading}
          onPress={() => {
            if (canAct()) void load(nextCursor);
          }}
        >
          Load more runs
        </Button>
      )}
    </section>
  );
}
