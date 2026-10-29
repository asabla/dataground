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
import { useDiscoveryPage } from "../discovery/useDiscoveryPage";
import {
  compareSearchCreatedAt,
  type SearchKind,
  type SearchResult,
  searchKindLabels,
  searchKinds,
  searchResources,
  validSearchText,
} from "./client";

export function ResourceSearch({
  client,
  isolationDomainId,
  onOpen,
}: {
  client: DataGroundClient;
  isolationDomainId: string;
  onOpen: (item: SearchResult) => void;
}) {
  const [query, setQuery] = useState("");
  const [appliedQuery, setAppliedQuery] = useState("");
  const [kind, setKind] = useState<SearchKind | "all">("all");
  const [sort, setSort] = useState<DataTableSort>({ column: "created", direction: "descending" });
  const read = useCallback(
    (cursor?: string) =>
      appliedQuery
        ? searchResources(
            client,
            isolationDomainId,
            appliedQuery,
            kind === "all" ? undefined : kind,
            cursor,
          )
        : Promise.resolve({ ok: true as const, page: { items: [] as SearchResult[] } }),
    [client, isolationDomainId, appliedQuery, kind],
  );
  const page = useDiscoveryPage(read, "Search did not advance. Refresh search to recover.");
  const resultStatus = useRef<HTMLParagraphElement>(null);
  const moreRequested = useRef(false);
  useEffect(() => {
    if (page.loading || !moreRequested.current) return;
    moreRequested.current = false;
    // The last continuation removes its button. Do not leave keyboard focus on
    // the document, or take it from a user who moved to another control.
    if (
      document.activeElement === document.body &&
      !resultStatus.current?.closest("[hidden],[inert]")
    )
      resultStatus.current?.focus();
  }, [page]);
  const visible = [...page.items].sort(
    (a, b) =>
      (sort.direction === "descending" ? -1 : 1) *
      (sort.column === "name"
        ? a.name.localeCompare(b.name)
        : compareSearchCreatedAt(a.metadata.createdAt, b.metadata.createdAt)),
  );
  return (
    <section aria-labelledby="resource-search-title">
      <div className="workbench-page-heading">
        <div>
          <p className="workbench-kicker">Discovery</p>
          <h1 id="resource-search-title" tabIndex={-1}>
            Search resources
          </h1>
          <p>Find workspaces, agent services, runs and artifacts in this scope.</p>
        </div>
      </div>
      <form
        className="resource-browser__toolbar resource-browser__toolbar--search"
        onSubmit={(event) => {
          event.preventDefault();
          const value = query.trim();
          if (!validSearchText(value)) return;
          if (value === appliedQuery) void page.load();
          else setAppliedQuery(value);
        }}
      >
        <TextField
          label="Search text"
          name="resource-search"
          type="search"
          description="Search names, descriptions or resource IDs. File and result contents are excluded."
          value={query}
          onChange={setQuery}
          errorMessage={
            query.trim() && !validSearchText(query.trim())
              ? "Use at most 128 characters without control characters."
              : undefined
          }
        />
        <SelectField
          label="Resource kind"
          value={kind}
          options={[
            { value: "all", label: "All resources" },
            ...searchKinds.map((value) => ({ value, label: searchKindLabels[value] })),
          ]}
          onChange={(value) => {
            if (value === "all" || searchKinds.includes(value as SearchKind))
              setKind(value as SearchKind | "all");
          }}
        />
        <Button type="submit" variant="primary" isDisabled={!validSearchText(query.trim())}>
          Search
        </Button>
        <Button
          variant="quiet"
          isDisabled={!appliedQuery || page.loading}
          onPress={() => void page.load()}
        >
          Refresh search
        </Button>
      </form>
      {!appliedQuery ? (
        <p>
          Enter a name or resource ID to start. Opening a result requires access to that resource.
        </p>
      ) : (
        <>
          <p role="status" tabIndex={-1} ref={resultStatus}>
            {page.loading
              ? "Searching resources…"
              : page.error
                ? "Search could not be completed."
                : `${page.items.length} ${page.items.length === 1 ? "result" : "results"} loaded for “${appliedQuery}”. Sort applies to loaded results.`}
          </p>
          {page.error && (
            <p role="alert" className="workbench-inline-error">
              {page.error.message}
            </p>
          )}
          {!page.loading && !page.error && (
            <DataTable
              label="Search results"
              items={visible}
              getRowId={(item) => item.metadata.id}
              getRowLabel={(item) => `${searchKindLabels[item.kind]} ${item.name}`}
              sort={sort}
              onSortChange={setSort}
              emptyMessage="No resources match this search."
              columns={[
                {
                  id: "name",
                  title: "Name",
                  isRowHeader: true,
                  allowsSorting: true,
                  render: (item) => (
                    <div className="resource-search__identity">
                      <strong>{item.name}</strong>
                      {"description" in item && item.description && <p>{item.description}</p>}
                    </div>
                  ),
                },
                { id: "kind", title: "Kind", render: (item) => searchKindLabels[item.kind] },
                {
                  id: "state",
                  title: "State",
                  render: (item) =>
                    "state" in item ? (
                      <StatusBadge
                        tone={
                          item.state === "failed"
                            ? "critical"
                            : item.state === "waiting" || item.state === "unknown"
                              ? "warning"
                              : "neutral"
                        }
                      >
                        {item.state}
                      </StatusBadge>
                    ) : (
                      "—"
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
                  id: "actions",
                  title: "Actions",
                  render: (item) => (
                    <Button
                      variant="quiet"
                      aria-label={`Open ${searchKindLabels[item.kind].toLowerCase()} ${item.name}`}
                      onPress={() => {
                        if (page.canAct()) onOpen(item);
                      }}
                    >
                      Open
                    </Button>
                  ),
                },
              ]}
            />
          )}
          {page.nextCursor && !page.error && (
            <Button
              variant="quiet"
              isDisabled={page.loading}
              onPress={() => {
                if (page.canAct()) {
                  moreRequested.current = true;
                  void page.load(page.nextCursor);
                }
              }}
            >
              Load more results
            </Button>
          )}
        </>
      )}
    </section>
  );
}
