import { Button, CopyButton, DataTable, type DataTableSort, TextField } from "@dataground/ui";
import { useCallback, useEffect, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import { useDiscoveryPage } from "../discovery/useDiscoveryPage";
import { AgentServiceCreateWorkflow } from "../services/AgentServiceCreateWorkflow";
import { type AgentServiceFailure, listAgentServices } from "../services/client";
import { listWorkspaces, readWorkspace, type WorkspaceResource } from "./client";
import { WorkspaceCreate } from "./WorkspaceCreate";

interface Props {
  client: DataGroundClient;
  isolationDomainId: string;
  workspaceId?: string;
  active: boolean;
  onNavigate: (id?: string) => void;
  onOpenService: (id: string) => void;
}
export function WorkspaceBrowser(props: Props) {
  const { client, isolationDomainId, workspaceId, active } = props;
  const current = useRef(props);
  current.current = props;
  const [visited, setVisited] = useState<string[]>(workspaceId ? [workspaceId] : []);
  if (workspaceId && !visited.includes(workspaceId)) setVisited([...visited, workspaceId]);
  return (
    <>
      <div hidden={!!workspaceId} inert={!!workspaceId}>
        <WorkspaceList
          client={client}
          isolationDomainId={isolationDomainId}
          onNavigate={(id) => {
            if (current.current.active && current.current.workspaceId === undefined)
              current.current.onNavigate(id);
          }}
        />
      </div>
      {visited.map((id) => (
        <div key={id} hidden={workspaceId !== id} inert={workspaceId !== id}>
          <WorkspaceDetail
            {...props}
            onNavigate={(next) => {
              if (current.current.active && current.current.workspaceId === id)
                current.current.onNavigate(next);
            }}
            onOpenService={(serviceId) => {
              if (current.current.active && current.current.workspaceId === id)
                current.current.onOpenService(serviceId);
            }}
            workspaceId={id}
            active={active && workspaceId === id}
          />
        </div>
      ))}
    </>
  );
}
function WorkspaceList({
  client,
  isolationDomainId,
  onNavigate,
}: Pick<Props, "client" | "isolationDomainId" | "onNavigate">) {
  const [query, setQuery] = useState(""),
    [creating, setCreating] = useState(false),
    [creation, setCreation] = useState(0),
    [sort, setSort] = useState<DataTableSort>({ column: "created", direction: "descending" });
  const read = useCallback(
    (cursor?: string) => listWorkspaces(client, isolationDomainId, cursor),
    [client, isolationDomainId],
  );
  const page = useDiscoveryPage(read, "Workspace discovery did not advance. Refresh to recover.");
  const visible = page.items
    .filter((w) =>
      `${w.name} ${w.description ?? ""}`.toLocaleLowerCase().includes(query.toLocaleLowerCase()),
    )
    .sort(
      (a, b) =>
        (sort.direction === "descending" ? -1 : 1) *
        (sort.column === "name"
          ? a.name.localeCompare(b.name)
          : Date.parse(a.metadata.createdAt) - Date.parse(b.metadata.createdAt)),
    );
  return (
    <section aria-labelledby="workspaces-title">
      <div className="workbench-page-heading">
        <div>
          <p className="workbench-kicker">Organization</p>
          <h1 id="workspaces-title" tabIndex={-1}>
            Workspace
          </h1>
          <p>Group related agent work and return to its services.</p>
        </div>
        <Button variant="primary" onPress={() => setCreating((v) => !v)}>
          {creating ? "Hide creation form" : "New workspace"}
        </Button>
      </div>
      <div hidden={!creating} inert={!creating}>
        <WorkspaceCreate
          key={creation}
          client={client}
          isolationDomainId={isolationDomainId}
          onCreated={(w) => {
            setCreating(false);
            setCreation((v) => v + 1);
            void page.load();
            onNavigate(w.metadata.id);
          }}
        />
      </div>
      <div className="resource-browser__toolbar">
        <TextField
          label="Search loaded workspaces"
          type="search"
          value={query}
          onChange={setQuery}
        />
        <Button variant="quiet" isDisabled={page.loading} onPress={() => void page.load()}>
          Refresh workspaces
        </Button>
        <p>
          {visible.length} of {page.items.length} loaded workspaces shown.
        </p>
      </div>
      {page.loading && <p role="status">Loading workspaces…</p>}
      {page.error && <p role="alert">{page.error.message}</p>}
      {!page.loading && !page.error && (
        <DataTable
          label="Workspaces"
          items={visible}
          getRowId={(w) => w.metadata.id}
          getRowLabel={(w) => w.name}
          sort={sort}
          onSortChange={setSort}
          emptyMessage={
            page.items.length
              ? "No loaded workspaces match this search."
              : "No workspaces are available in this scope."
          }
          columns={[
            {
              id: "name",
              title: "Name",
              isRowHeader: true,
              allowsSorting: true,
              render: (w) => <strong>{w.name}</strong>,
            },
            { id: "description", title: "Description", render: (w) => w.description ?? "—" },
            {
              id: "created",
              title: "Created",
              allowsSorting: true,
              render: (w) => (
                <time dateTime={w.metadata.createdAt}>
                  {new Date(w.metadata.createdAt).toLocaleString()}
                </time>
              ),
            },
            {
              id: "actions",
              title: "Actions",
              render: (w) => (
                <Button
                  variant="quiet"
                  aria-label={`Open workspace ${w.name}`}
                  onPress={() => {
                    if (page.canAct()) onNavigate(w.metadata.id);
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
            if (page.canAct()) void page.load(page.nextCursor);
          }}
        >
          Load more workspaces
        </Button>
      )}
    </section>
  );
}
function WorkspaceDetail({
  client,
  isolationDomainId,
  workspaceId,
  active,
  onNavigate,
  onOpenService,
}: Props & { workspaceId: string }) {
  const [workspace, setWorkspace] = useState<WorkspaceResource>(),
    [error, setError] = useState<AgentServiceFailure>(),
    [loading, setLoading] = useState(true),
    [reload, setReload] = useState(0),
    [opened, setOpened] = useState(false);
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    let current = true;
    void reload;
    setWorkspace(undefined);
    setError(undefined);
    setLoading(true);
    void readWorkspace(client, isolationDomainId, workspaceId).then((r) => {
      if (!current) return;
      setLoading(false);
      if (r.ok) {
        setWorkspace(r.workspace);
        setOpened(true);
      } else setError(r.error);
    });
    return () => {
      current = false;
    };
  }, [client, isolationDomainId, workspaceId, reload]);
  useEffect(() => {
    if (active && !loading) heading.current?.focus();
  }, [active, loading]);
  return (
    <section>
      <Button variant="quiet" onPress={() => onNavigate()}>
        All workspaces
      </Button>
      <div className="workbench-page-heading">
        <div>
          <p className="workbench-kicker">Workspace</p>
          <h1 ref={heading} tabIndex={-1}>
            {workspace?.name ?? "Workspace"}
          </h1>
          {workspace?.description && <p>{workspace.description}</p>}
        </div>
        <Button variant="quiet" isDisabled={loading} onPress={() => setReload((v) => v + 1)}>
          Refresh workspace
        </Button>
      </div>
      {loading && <p role="status">Loading workspace…</p>}
      {error && <p role="alert">{error.message}</p>}
      {opened && (
        <div hidden={!workspace || !!error || loading} inert={!workspace || !!error || loading}>
          <WorkspaceServices
            client={client}
            isolationDomainId={isolationDomainId}
            workspaceId={workspaceId}
            onOpenService={onOpenService}
            enabled={active && !!workspace && !error && !loading}
          />
        </div>
      )}
      {workspace && !error && !loading && (
        <details>
          <summary>Workspace details</summary>
          <dl>
            <dt>Workspace ID</dt>
            <dd>
              <code>{workspace.metadata.id}</code>
              <CopyButton value={workspace.metadata.id} label="Copy workspace ID" />
            </dd>
            <dt>Created by</dt>
            <dd>{workspace.metadata.createdBy}</dd>
          </dl>
          <p>
            This relationship organizes work. Service, source and execution permissions are
            separate.
          </p>
        </details>
      )}
    </section>
  );
}
function WorkspaceServices({
  client,
  isolationDomainId,
  workspaceId,
  onOpenService,
  enabled,
}: Pick<Props, "client" | "isolationDomainId" | "onOpenService"> & {
  workspaceId: string;
  enabled: boolean;
}) {
  const [query, setQuery] = useState("");
  const [appliedQuery, setAppliedQuery] = useState("");
  const read = useCallback(
    (cursor?: string) =>
      listAgentServices(client, isolationDomainId, cursor, workspaceId, appliedQuery),
    [client, isolationDomainId, workspaceId, appliedQuery],
  );
  const page = useDiscoveryPage(
    read,
    "Workspace service discovery did not advance. Refresh to recover.",
  );
  const [creating, setCreating] = useState(false),
    [creation, setCreation] = useState(0),
    [sort, setSort] = useState<DataTableSort>({ column: "created", direction: "descending" });
  const [createdName, setCreatedName] = useState<string>();
  const serviceHeading = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    if (createdName && enabled) serviceHeading.current?.focus();
  }, [createdName, enabled]);
  const currentEnabled = useRef(enabled);
  currentEnabled.current = enabled;
  return (
    <>
      <div className="workbench-page-heading">
        <h2 ref={serviceHeading} tabIndex={-1}>
          Agent services
        </h2>
        <Button onPress={() => setCreating((v) => !v)}>
          {creating ? "Hide service form" : "New service in workspace"}
        </Button>
      </div>
      {createdName && <p role="status">Created {createdName} in this workspace.</p>}
      <div hidden={!creating} inert={!creating}>
        <AgentServiceCreateWorkflow
          key={creation}
          compact
          canCreate={enabled}
          client={client}
          isolationDomainId={isolationDomainId}
          workspaceId={workspaceId}
          onServiceCreated={(service) => {
            setCreatedName(service.name);
            setCreating(false);
            setCreation((v) => v + 1);
            void page.load();
          }}
          onOpenService={(s) => onOpenService(s.metadata.id)}
        />
      </div>
      <form
        className="resource-browser__toolbar resource-browser__toolbar--search"
        onSubmit={(event) => {
          event.preventDefault();
          const value = query.trim();
          if (value === appliedQuery) void page.load();
          else setAppliedQuery(value);
        }}
      >
        <TextField
          label="Search workspace services"
          description="Search by name, description, or service ID."
          maxLength={128}
          type="search"
          value={query}
          onChange={setQuery}
        />
        <Button type="submit">Search</Button>
        <Button variant="quiet" isDisabled={page.loading} onPress={() => void page.load()}>
          Refresh workspace services
        </Button>
      </form>
      <p role="status">
        {page.items.length} services loaded{appliedQuery ? ` for “${appliedQuery}”` : ""}. Sort
        applies to loaded results.
      </p>
      {page.loading && <p role="status">Loading workspace services…</p>}
      {page.error && <p role="alert">{page.error.message}</p>}
      {!page.loading && !page.error && (
        <DataTable
          label="Workspace agent services"
          items={[...page.items].sort(
            (a, b) =>
              (sort.direction === "descending" ? -1 : 1) *
              (sort.column === "name"
                ? a.name.localeCompare(b.name)
                : Date.parse(a.metadata.createdAt) - Date.parse(b.metadata.createdAt)),
          )}
          sort={sort}
          onSortChange={setSort}
          getRowId={(s) => s.metadata.id}
          getRowLabel={(s) => s.name}
          emptyMessage={
            appliedQuery
              ? "No services match this search."
              : "No agent services have been created in this workspace."
          }
          columns={[
            {
              id: "name",
              title: "Name",
              isRowHeader: true,
              allowsSorting: true,
              render: (s) => <strong>{s.name}</strong>,
            },
            { id: "description", title: "Description", render: (s) => s.description ?? "—" },
            {
              id: "created",
              title: "Created",
              allowsSorting: true,
              render: (s) => (
                <time dateTime={s.metadata.createdAt}>
                  {new Date(s.metadata.createdAt).toLocaleString()}
                </time>
              ),
            },
            {
              id: "actions",
              title: "Actions",
              render: (s) => (
                <Button
                  variant="quiet"
                  aria-label={`Open service ${s.name}`}
                  onPress={() => {
                    if (currentEnabled.current && page.canAct()) onOpenService(s.metadata.id);
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
            if (page.canAct()) void page.load(page.nextCursor);
          }}
        >
          Load more workspace services
        </Button>
      )}
    </>
  );
}
