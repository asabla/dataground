import {
  ActionMenu,
  Button,
  DataTable,
  type DataTableSort,
  SelectField,
  StatusBadge,
} from "@dataground/ui";
import { useId, useState } from "react";
import type { ServiceRevisionFailure, ServiceRevisionHistoryResource } from "./client";

export interface ServiceRevisionHistoryPanelProps {
  titleId?: string;
  error?: ServiceRevisionFailure;
  isLoading: boolean;
  isLoadingMore: boolean;
  nextCursor?: string;
  onLoadMore: () => void;
  onRetry: () => void;
  onCreate?: () => void;
  onOpen?: (revision: ServiceRevisionHistoryResource) => void;
  onInspectAudit?: (revision: ServiceRevisionHistoryResource) => void;
  onRetire?: (revision: ServiceRevisionHistoryResource) => void;
  revisions: ServiceRevisionHistoryResource[];
}

function revisionTone(state: ServiceRevisionHistoryResource["state"]) {
  if (state === "published") return "success" as const;
  if (state === "retired") return "neutral" as const;
  return "warning" as const;
}

export function ServiceRevisionHistoryPanel({
  titleId: suppliedTitleId,
  error,
  isLoading,
  isLoadingMore,
  nextCursor,
  onLoadMore,
  onRetry,
  onRetire,
  onInspectAudit,
  onCreate,
  onOpen,
  revisions,
}: ServiceRevisionHistoryPanelProps) {
  const [sort, setSort] = useState<DataTableSort>({ column: "version", direction: "descending" });
  const [stateFilter, setStateFilter] = useState("all");
  const visibleRevisions = revisions
    .filter((revision) => stateFilter === "all" || revision.state === stateFilter)
    .sort((a, b) => {
      const comparison =
        sort.column === "version"
          ? a.revisionNumber - b.revisionNumber
          : a.runtimeProfile.localeCompare(b.runtimeProfile);
      return (sort.direction === "ascending" ? 1 : -1) * comparison;
    });
  const generatedTitleId = useId();
  const titleId = suppliedTitleId ?? generatedTitleId;
  return (
    <section aria-labelledby={titleId} className="revision-history">
      <div className="revision-history__heading">
        <div>
          <p className="workbench-kicker">Authoritative history</p>
          <h2 id={titleId} tabIndex={-1}>
            Service revisions
          </h2>
        </div>
        <div>
          {revisions.length > 0 && <span>{revisions.length} loaded</span>}
          {onCreate && (
            <Button
              isDisabled={isLoading || isLoadingMore || !!error}
              onPress={onCreate}
              variant="quiet"
            >
              New revision
            </Button>
          )}
        </div>
      </div>

      {error && (
        <div className="workbench-inline-error revision-history__error" role="alert">
          <p>{error.message}</p>
          <Button onPress={onRetry} variant="quiet">
            Retry revision discovery
          </Button>
        </div>
      )}

      {isLoading && revisions.length === 0 ? (
        <p aria-busy="true" className="revision-history__state">
          Loading revision history…
        </p>
      ) : revisions.length === 0 && !error ? (
        <p className="revision-history__state">
          No revisions yet. Use New revision to create the first definition.
        </p>
      ) : revisions.length > 0 ? (
        <>
          <div className="resource-browser__toolbar">
            <SelectField
              label="Version state"
              value={stateFilter}
              onChange={setStateFilter}
              options={[
                { value: "all", label: "All states" },
                { value: "draft", label: "Draft" },
                { value: "published", label: "Published" },
                { value: "retired", label: "Retired" },
              ]}
            />
            <p>
              {visibleRevisions.length} of {revisions.length} loaded versions shown.
            </p>
          </div>
          <DataTable
            sort={sort}
            onSortChange={setSort}
            label="Loaded service versions"
            items={visibleRevisions}
            getRowId={(revision) => revision.metadata.id}
            getRowLabel={(revision) => `Revision ${revision.revisionNumber}`}
            isLoading={isLoading || isLoadingMore}
            emptyMessage="No loaded versions match this state."
            columns={[
              {
                id: "version",
                title: "Version",
                allowsSorting: true,
                isRowHeader: true,
                render: (revision) => <strong>Revision {revision.revisionNumber}</strong>,
              },
              {
                id: "state",
                title: "State",
                render: (revision) => (
                  <StatusBadge tone={revisionTone(revision.state)}>{revision.state}</StatusBadge>
                ),
              },
              {
                id: "runtime",
                title: "Runtime",
                allowsSorting: true,
                render: (revision) => revision.runtimeProfile,
              },
              {
                id: "updated",
                title: "Record updated",
                render: (revision) => (
                  <time dateTime={revision.metadata.updatedAt}>
                    {new Date(revision.metadata.updatedAt).toLocaleString()}
                  </time>
                ),
              },
              {
                id: "actions",
                title: "Actions",
                render: (revision) => (
                  <div className="resource-browser__actions">
                    {onOpen && revision.state !== "retired" && (
                      <Button
                        aria-label={`Open revision ${revision.revisionNumber}`}
                        isDisabled={isLoading || isLoadingMore || !!error}
                        variant="quiet"
                        onPress={() => onOpen(revision)}
                      >
                        Open
                      </Button>
                    )}
                    {(onInspectAudit || (onRetire && revision.state === "published")) && (
                      <ActionMenu
                        label={`More actions for revision ${revision.revisionNumber}`}
                        isDisabled={isLoading || isLoadingMore || !!error}
                        items={[
                          ...(onInspectAudit
                            ? [
                                {
                                  id: "audit",
                                  label: `Inspect audit for revision ${revision.revisionNumber}`,
                                },
                              ]
                            : []),
                          ...(onRetire && revision.state === "published"
                            ? [
                                {
                                  id: "retire",
                                  label: `Retire revision ${revision.revisionNumber}`,
                                },
                              ]
                            : []),
                        ]}
                        onAction={(action) => {
                          if (isLoading || isLoadingMore || error) return;
                          if (action === "audit") onInspectAudit?.(revision);
                          if (action === "retire" && revision.state === "published")
                            onRetire?.(revision);
                        }}
                      />
                    )}
                  </div>
                ),
              },
            ]}
          />
          {nextCursor && (
            <div className="revision-history__more">
              <Button isDisabled={isLoadingMore} onPress={onLoadMore} variant="quiet">
                {isLoadingMore ? "Loading more revisions…" : "Load more revisions"}
              </Button>
            </div>
          )}
        </>
      ) : null}
    </section>
  );
}
