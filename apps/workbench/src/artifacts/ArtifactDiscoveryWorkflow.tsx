import {
  Button,
  DataTable,
  type DataTableSort,
  SelectField,
  StatusBadge,
  TextField,
} from "@dataground/ui";
import { useCallback, useId, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import { useDiscoveryPage } from "../discovery/useDiscoveryPage";
import {
  type InvocationArtifact,
  type InvocationArtifactListReference,
  type InvocationArtifactReference,
  listInvocationArtifacts,
} from "./client";

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
  const titleId = useId();
  const [stateFilter, setStateFilter] = useState("all");
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<DataTableSort>({ column: "created", direction: "descending" });
  const read = useCallback(
    (cursor?: string) =>
      listInvocationArtifacts(client, { isolationDomainId, invocationId }, cursor),
    [client, isolationDomainId, invocationId],
  );
  const { items, loading, error, nextCursor, load, canAct } = useDiscoveryPage(
    read,
    "Artifact discovery did not advance. Refresh artifacts to recover.",
  );
  const inspect = (item: InvocationArtifact) => {
    if (canAct())
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
      {error && (
        <div role="alert" className="workbench-inline-error">
          <p>{error.message}</p>
        </div>
      )}
      {!loading && !error && items.length === 0 && <p>No artifacts recorded for this run.</p>}
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
      {nextCursor && !error && (
        <Button
          variant="quiet"
          isDisabled={loading}
          onPress={() => {
            if (canAct()) void load(nextCursor);
          }}
        >
          Load more artifacts
        </Button>
      )}
    </section>
  );
}
