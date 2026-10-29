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
  type ArtifactFailure,
  listScopeArtifacts,
  type ScopeArtifact,
  type ScopeArtifactState,
  scopeArtifactStates,
} from "./client";

interface ArtifactPage {
  client: DataGroundClient;
  scope: string;
  filter: string;
  loading: boolean;
  items: ScopeArtifact[];
  cursors: string[];
  nextCursor?: string;
  error?: ArtifactFailure;
}
export function ScopeArtifactBrowser({
  client,
  isolationDomainId,
  onOpen,
}: {
  client: DataGroundClient;
  isolationDomainId: string;
  onOpen: (artifact: ScopeArtifact) => void;
}) {
  const [filter, setFilter] = useState<ScopeArtifactState | "all">("all");
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<DataTableSort>({ column: "created", direction: "descending" });
  const generation = useRef(0);
  const busy = useRef(false);
  const identity = useRef({ client, isolationDomainId, filter });
  identity.current = { client, isolationDomainId, filter };
  const [page, setPage] = useState<ArtifactPage>({
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
      const result = await listScopeArtifacts(
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
              code: "WORKBENCH_ARTIFACT_DISCOVERY_STALLED",
              message: "Artifact discovery did not advance. Refresh artifacts to recover.",
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
      `${item.name} ${item.serviceName}`.toLocaleLowerCase().includes(query.toLocaleLowerCase()),
    )
    .sort(
      (a, b) =>
        (sort.direction === "descending" ? -1 : 1) *
        (sort.column === "name"
          ? a.name.localeCompare(b.name)
          : Date.parse(a.metadata.createdAt) - Date.parse(b.metadata.createdAt)),
    );
  const open = (item: ScopeArtifact) => {
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
    <section aria-labelledby="scope-artifacts-title">
      <div className="workbench-page-heading">
        <div>
          <p className="workbench-kicker">Outputs</p>
          <h1 id="scope-artifacts-title" tabIndex={-1}>
            Artifacts
          </h1>
          <p>Find outputs across runs in this scope. File content requires separate access.</p>
        </div>
        <Button variant="quiet" isDisabled={loading} onPress={() => void load()}>
          Refresh artifacts
        </Button>
      </div>
      <div className="resource-browser__toolbar">
        <TextField
          label="Search loaded names and services"
          type="search"
          value={query}
          onChange={setQuery}
        />
        <SelectField
          label="Artifact state"
          value={filter}
          onChange={(value) => {
            if (value === "all" || scopeArtifactStates.includes(value as ScopeArtifactState))
              setFilter(value as ScopeArtifactState | "all");
          }}
          options={[
            { value: "all", label: "All states" },
            ...scopeArtifactStates.map((value) => ({ value, label: value })),
          ]}
        />
        <p>
          {visible.length} of {items.length} loaded artifacts shown.
        </p>
      </div>
      {loading && <p role="status">Loading artifacts…</p>}
      {current && page.error && (
        <p role="alert" className="workbench-inline-error">
          {page.error.message}
        </p>
      )}
      {!loading && !page.error && (
        <DataTable
          label="Artifacts across runs"
          items={visible}
          getRowId={(item) => item.metadata.id}
          getRowLabel={(item) => item.name}
          sort={sort}
          onSortChange={setSort}
          emptyMessage={
            items.length
              ? "No loaded artifacts match this search."
              : "No artifacts match this state in the current scope."
          }
          columns={[
            {
              id: "name",
              title: "Name",
              isRowHeader: true,
              allowsSorting: true,
              render: (item) => <strong>{item.name}</strong>,
            },
            { id: "service", title: "Source service", render: (item) => item.serviceName },
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
            { id: "format", title: "Format", render: (item) => item.mediaType },
            {
              id: "size",
              title: "Size",
              render: (item) => `${item.sizeBytes.toLocaleString()} bytes`,
            },
            {
              id: "sensitive",
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
          Load more artifacts
        </Button>
      )}
    </section>
  );
}
