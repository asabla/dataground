import { Button, DataTable, SelectField, StatusBadge } from "@dataground/ui";
import { useCallback, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import { useDiscoveryPage } from "../discovery/useDiscoveryPage";
import { listScopeInvocations, type ScopeInvocationSummary } from "../invocations/client";

export interface RecentWork {
  key: string;
  name: string;
  kind: "Agent service" | "Unsaved service draft";
}

export function WorkbenchHome({
  client,
  isolationDomainId,
  recentWork,
  onOpenWork,
  onOpenRun,
  onCreate,
  onBrowseRuns,
}: {
  client: DataGroundClient;
  isolationDomainId: string;
  recentWork: readonly RecentWork[];
  onOpenWork: (key: string) => void;
  onOpenRun: (run: ScopeInvocationSummary) => void;
  onCreate: () => void;
  onBrowseRuns: () => void;
}) {
  const [state, setState] = useState<"waiting" | "failed" | "unknown">("waiting");
  const read = useCallback(
    (cursor?: string) => listScopeInvocations(client, isolationDomainId, state, cursor),
    [client, isolationDomainId, state],
  );
  const { items, loading, error, nextCursor, load, canAct } = useDiscoveryPage(
    read,
    "Attention discovery did not advance. Refresh the list to recover.",
  );
  const shown = items.slice(0, 8);
  return (
    <section aria-labelledby="workbench-home-title">
      <div className="workbench-page-heading">
        <div>
          <p className="workbench-kicker">Workbench</p>
          <h1 id="workbench-home-title" tabIndex={-1}>
            Home
          </h1>
          <p>Resume your work or inspect an execution that needs attention.</p>
        </div>
        <Button onPress={onCreate} variant="primary">
          New service
        </Button>
      </div>
      <section className="workbench-home-section" aria-labelledby="home-recent-title">
        <h2 id="home-recent-title">Recent work</h2>
        <p>Opened in this connection, most recent first. Unsaved drafts remain in this session.</p>
        <DataTable
          label="Recent work in this connection"
          items={recentWork}
          getRowId={(item) => item.key}
          getRowLabel={(item) => item.name}
          emptyMessage="No work opened in this connection yet. Open an agent service or create one."
          columns={[
            {
              id: "name",
              title: "Name",
              isRowHeader: true,
              render: (item) => <strong>{item.name}</strong>,
            },
            { id: "kind", title: "Type", render: (item) => item.kind },
            {
              id: "action",
              title: "Actions",
              render: (item) => (
                <Button
                  variant="quiet"
                  aria-label={`Resume ${item.name}`}
                  onPress={() => onOpenWork(item.key)}
                >
                  Resume
                </Button>
              ),
            },
          ]}
        />
      </section>
      <section className="workbench-home-section" aria-labelledby="home-attention-title">
        <h2 id="home-attention-title">Needs attention</h2>
        <p>Inspect a run to see its current blocker and available action.</p>
        <div className="resource-browser__toolbar">
          <SelectField
            label="Attention state"
            value={state}
            options={[
              { value: "waiting", label: "Waiting" },
              { value: "failed", label: "Failed" },
              { value: "unknown", label: "Unconfirmed" },
            ]}
            onChange={(value) => {
              if (value === "waiting" || value === "failed" || value === "unknown") setState(value);
            }}
          />
          <Button variant="quiet" isDisabled={loading} onPress={() => void load()}>
            Refresh attention
          </Button>
          <Button variant="quiet" onPress={onBrowseRuns}>
            View all runs
          </Button>
        </div>
        {loading && <p role="status">Loading attention items…</p>}
        {error && (
          <p role="alert" className="workbench-inline-error">
            {error.message}
          </p>
        )}
        {!loading && !error && (
          <>
            <DataTable
              label="Runs that need attention"
              items={shown}
              getRowId={(item) => item.metadata.id}
              getRowLabel={(item) => `${item.serviceName}, ${item.alias}, ${item.state}`}
              emptyMessage="No runs in this attention state in the current scope."
              columns={[
                {
                  id: "service",
                  title: "Service",
                  isRowHeader: true,
                  render: (item) => <strong>{item.serviceName}</strong>,
                },
                { id: "route", title: "Route", render: (item) => item.alias },
                {
                  id: "state",
                  title: "State",
                  render: (item) => (
                    <StatusBadge tone={item.state === "failed" ? "critical" : "warning"}>
                      {item.state === "unknown" ? "Unconfirmed" : item.state}
                    </StatusBadge>
                  ),
                },
                {
                  id: "created",
                  title: "Created",
                  render: (item) => (
                    <time dateTime={item.metadata.createdAt}>
                      {new Date(item.metadata.createdAt).toLocaleString()}
                    </time>
                  ),
                },
                {
                  id: "action",
                  title: "Actions",
                  render: (item) => (
                    <Button
                      variant="quiet"
                      aria-label={`Inspect ${item.serviceName}, ${item.alias}, ${item.state}`}
                      onPress={() => {
                        if (canAct()) onOpenRun(item);
                      }}
                    >
                      Inspect
                    </Button>
                  ),
                },
              ]}
            />
            {(items.length > shown.length || nextCursor) && (
              <p>
                Showing the first {shown.length} loaded runs. Open Runs for the full list and
                filters.
              </p>
            )}
          </>
        )}
      </section>
    </section>
  );
}
