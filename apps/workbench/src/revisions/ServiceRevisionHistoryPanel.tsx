import { Button, StatusBadge } from "@dataground/ui";
import type { ServiceRevisionFailure, ServiceRevisionHistoryResource } from "./client";

export interface ServiceRevisionHistoryPanelProps {
  error?: ServiceRevisionFailure;
  isLoading: boolean;
  isLoadingMore: boolean;
  nextCursor?: string;
  onLoadMore: () => void;
  onRetry: () => void;
  revisions: ServiceRevisionHistoryResource[];
}

function revisionTone(state: ServiceRevisionHistoryResource["state"]) {
  if (state === "published") return "success" as const;
  if (state === "retired") return "neutral" as const;
  return "warning" as const;
}

export function ServiceRevisionHistoryPanel({
  error,
  isLoading,
  isLoadingMore,
  nextCursor,
  onLoadMore,
  onRetry,
  revisions,
}: ServiceRevisionHistoryPanelProps) {
  return (
    <section aria-labelledby="revision-history-title" className="revision-history">
      <div className="revision-history__heading">
        <div>
          <p className="workbench-kicker">Authoritative history</p>
          <h2 id="revision-history-title">Service revisions</h2>
        </div>
        {revisions.length > 0 && <span>{revisions.length} loaded</span>}
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
          No revisions yet. Create the first immutable service definition below.
        </p>
      ) : revisions.length > 0 ? (
        <>
          <ol className="revision-history__list">
            {revisions.map((revision) => (
              <li key={revision.metadata.id}>
                <span className="revision-history__number">Revision {revision.revisionNumber}</span>
                <StatusBadge tone={revisionTone(revision.state)}>{revision.state}</StatusBadge>
                <code>{revision.runtimeProfile}</code>
                <span>
                  Updated{" "}
                  <time dateTime={revision.metadata.updatedAt}>{revision.metadata.updatedAt}</time>
                </span>
              </li>
            ))}
          </ol>
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
