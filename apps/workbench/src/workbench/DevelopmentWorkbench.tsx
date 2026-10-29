import { Button } from "@dataground/ui";
import { useCallback, useEffect, useRef, useState } from "react";
import { ScopeArtifactBrowser } from "../artifacts/ScopeArtifactBrowser";
import type { DataGroundClient } from "../contracts/client";
import { useDiscoveryPage } from "../discovery/useDiscoveryPage";
import { ScopeRunBrowser } from "../invocations/ScopeRunBrowser";
import { ResourceSearch } from "../search/ResourceSearch";
import {
  type AgentServiceFailure,
  type AgentServiceResource,
  listAgentServices,
  readAgentService,
} from "../services/client";
import type { ServiceSort } from "../services/directory";
import { ServiceDirectory } from "../services/ServiceDirectory";
import { WorkspaceBrowser } from "../workspaces/WorkspaceBrowser";
import type { ServiceTab, WorkbenchRoute, WorkbenchView } from "./navigation";
import { ServiceWorkflowSession } from "./ServiceWorkflowSession";
import { useWorkbenchNavigation } from "./useWorkbenchNavigation";
import { WorkbenchHome } from "./WorkbenchHome";
import { WorkbenchShell } from "./WorkbenchShell";

export interface DevelopmentWorkbenchProps {
  client: DataGroundClient;
  isolationDomainId: string;
  onDisconnect: () => void;
}

export function DevelopmentWorkbench(props: DevelopmentWorkbenchProps) {
  const [connection, setConnection] = useState({
    client: props.client,
    scope: props.isolationDomainId,
    generation: 0,
  });
  if (connection.client !== props.client || connection.scope !== props.isolationDomainId) {
    setConnection({
      client: props.client,
      scope: props.isolationDomainId,
      generation: connection.generation + 1,
    });
  }
  return <WorkbenchSession key={connection.generation} {...props} />;
}

interface OpenServiceWork {
  key: string;
  service?: AgentServiceResource;
}

export function WorkbenchSession({
  client,
  isolationDomainId,
  onDisconnect,
}: DevelopmentWorkbenchProps) {
  const [serviceQuery, setServiceQuery] = useState("");
  const [appliedServiceQuery, setAppliedServiceQuery] = useState("");
  const [serviceSort, setServiceSort] = useState<ServiceSort>("updated");
  const { route, navigate } = useWorkbenchNavigation(isolationDomainId);
  const [sessions, setSessions] = useState<OpenServiceWork[]>([]);
  const [recentKeys, setRecentKeys] = useState<string[]>([]);
  const [workspaceCreationRequest, setWorkspaceCreationRequest] = useState(0);
  const lastLocations = useRef(new Map<string, WorkbenchRoute>());
  const retainedSessions = useRef(sessions);
  retainedSessions.current = sessions;
  const nextDraft = useRef(0);
  const [lastSessionKey, setLastSessionKey] = useState<string>();
  const [serviceReadError, setServiceReadError] = useState<AgentServiceFailure>();
  const [serviceReadLoading, setServiceReadLoading] = useState(false);
  const serviceReadGeneration = useRef(0);
  const pageContent = useRef<HTMLDivElement>(null);
  const activeSession =
    route?.view === "services" ||
    route?.view === "artifacts" ||
    route?.view === "home" ||
    route?.view === "search" ||
    route?.view === "workspaces"
      ? undefined
      : route?.serviceId
        ? sessions.find((session) => session.service?.metadata.id === route.serviceId)
        : route?.view === "workflow"
          ? sessions.find((session) => !session.service)
          : undefined;
  useEffect(() => {
    if (activeSession)
      setRecentKeys((current) =>
        [activeSession.key, ...current.filter((key) => key !== activeSession.key)].slice(0, 12),
      );
  }, [activeSession]);
  useEffect(() => {
    if (activeSession && route) lastLocations.current.set(activeSession.key, route);
  }, [activeSession, route]);
  const view = route?.view ?? "services";
  const showHome = !!route && view === "home";
  const showSearch = !!route && view === "search";
  const [visitedSearch, setVisitedSearch] = useState(showSearch);
  if (showSearch && !visitedSearch) setVisitedSearch(true);
  const [visitedHome, setVisitedHome] = useState(showHome);
  if (showHome && !visitedHome) setVisitedHome(true);
  const showScopeRuns = !!route && view === "interactions" && !route.serviceId;
  const [visitedScopeRuns, setVisitedScopeRuns] = useState(showScopeRuns);
  if (showScopeRuns && !visitedScopeRuns) setVisitedScopeRuns(true);
  const showArtifacts = !!route && view === "artifacts";
  const [visitedArtifacts, setVisitedArtifacts] = useState(showArtifacts);
  if (showArtifacts && !visitedArtifacts) setVisitedArtifacts(true);
  const showWorkspaces = !!route && view === "workspaces";
  const [visitedWorkspaces, setVisitedWorkspaces] = useState(showWorkspaces);
  if (showWorkspaces && !visitedWorkspaces) setVisitedWorkspaces(true);
  const [lastWorkspaceId, setLastWorkspaceId] = useState<string | undefined>(route?.workspaceId);
  if (showWorkspaces && route?.workspaceId !== lastWorkspaceId)
    setLastWorkspaceId(route?.workspaceId);
  const resolvingService = !!route?.serviceId && !activeSession;

  const readServicePage = useCallback(
    (cursor?: string) =>
      listAgentServices(client, isolationDomainId, cursor, undefined, appliedServiceQuery),
    [client, isolationDomainId, appliedServiceQuery],
  );
  const servicePage = useDiscoveryPage(
    readServicePage,
    "Service discovery did not advance. Refresh to recover.",
  );

  const rememberService = useCallback((service: AgentServiceResource) => {
    setSessions((current) =>
      current.some((session) => session.service?.metadata.id === service.metadata.id)
        ? current
        : [...current, { key: service.metadata.id, service }],
    );
  }, []);

  useEffect(() => {
    const generation = ++serviceReadGeneration.current;
    setServiceReadError(undefined);
    setServiceReadLoading(false);
    if (
      !route ||
      route.view === "services" ||
      route.view === "artifacts" ||
      route.view === "home" ||
      route.view === "search" ||
      route.view === "workspaces"
    )
      return;
    if (!route.serviceId) {
      if (
        route.view === "workflow" &&
        !retainedSessions.current.some((session) => !session.service)
      ) {
        const key = `draft-${++nextDraft.current}`;
        setSessions((current) =>
          current.some((session) => !session.service) ? current : [...current, { key }],
        );
        setLastSessionKey(key);
      }
      return;
    }
    const retained = retainedSessions.current.find(
      (session) => session.service?.metadata.id === route.serviceId,
    );
    if (retained) {
      setLastSessionKey(retained.key);
      return;
    }
    setServiceReadLoading(true);
    void readAgentService(client, isolationDomainId, route.serviceId).then((result) => {
      if (generation !== serviceReadGeneration.current) return;
      setServiceReadLoading(false);
      if (!result.ok) {
        setServiceReadError(result.error);
        return;
      }
      rememberService(result.service);
      setLastSessionKey(result.service.metadata.id);
    });
    return () => {
      serviceReadGeneration.current++;
    };
  }, [client, isolationDomainId, route, rememberService]);

  useEffect(() => {
    if (resolvingService || serviceReadLoading) return;
    if (showSearch) {
      pageContent.current?.querySelector<HTMLElement>('input[name="resource-search"]')?.focus();
      return;
    }
    const id = !route
      ? "location-error-title"
      : serviceReadError
        ? "service-read-title"
        : view === "services"
          ? "services-title"
          : showHome
            ? "workbench-home-title"
            : showScopeRuns
              ? "scope-runs-title"
              : showArtifacts
                ? "scope-artifacts-title"
                : undefined;
    if (id) pageContent.current?.querySelector<HTMLElement>(`#${id}`)?.focus();
  }, [
    route,
    resolvingService,
    serviceReadLoading,
    serviceReadError,
    view,
    showScopeRuns,
    showArtifacts,
    showHome,
    showSearch,
  ]);

  const navigateToService = (service: AgentServiceResource, next: WorkbenchView = "workflow") => {
    rememberService(service);
    setLastSessionKey(
      sessions.find((session) => session.service?.metadata.id === service.metadata.id)?.key ??
        service.metadata.id,
    );
    navigate({
      isolationDomainId,
      serviceId: service.metadata.id,
      view: next,
      ...(next === "workflow" ? { serviceTab: "overview" as const } : {}),
    });
  };
  const navigateView = (next: WorkbenchView) => {
    const context = activeSession ?? sessions.find((session) => session.key === lastSessionKey);
    navigate({
      isolationDomainId,
      view: next,
      ...(next === "workspaces" && lastWorkspaceId ? { workspaceId: lastWorkspaceId } : {}),
      ...(next === "services" ||
      next === "home" ||
      next === "search" ||
      next === "workspaces" ||
      next === "artifacts" ||
      next === "interactions"
        ? {}
        : { serviceId: context?.service?.metadata.id }),
    });
  };
  useEffect(() => {
    if (showWorkspaces && !route?.workspaceId) {
      const draftHeading = pageContent.current?.querySelector<HTMLElement>(
        '[aria-label="Create workspace"] h2',
      );
      if (draftHeading && !draftHeading.closest("[hidden],[inert]")) draftHeading.focus();
      else pageContent.current?.querySelector<HTMLElement>("#workspaces-title")?.focus();
    }
  }, [showWorkspaces, route?.workspaceId]);
  const create = () => navigate({ isolationDomainId, view: "workflow" });
  const sessionRoute = (
    session: OpenServiceWork,
    next: WorkbenchView,
    tab?: ServiceTab,
    revisionId = route?.serviceId === session.service?.metadata.id ? route?.revisionId : undefined,
  ): WorkbenchRoute => ({
    isolationDomainId,
    view: next,
    ...(next === "workflow" && revisionId ? { revisionId } : {}),
    ...(next === "services" ? {} : { serviceId: session.service?.metadata.id }),
    ...(next === "workflow" && tab && tab !== "build" && tab !== "runs" ? { serviceTab: tab } : {}),
  });

  return (
    <WorkbenchShell
      isolationDomainId={isolationDomainId}
      onDisconnect={onDisconnect}
      view={view}
      onNavigate={navigateView}
      onCreate={(kind) => {
        if (kind === "service") create();
        else {
          setWorkspaceCreationRequest((value) => value + 1);
          navigate({ isolationDomainId, view: "workspaces" });
        }
      }}
    >
      <div className="workbench-main" ref={pageContent}>
        {sessions.length > 0 && (
          <nav aria-label="Open service work" className="workbench-open-work">
            {sessions.map((session) => (
              <Button
                key={session.key}
                variant="quiet"
                aria-current={activeSession?.key === session.key ? "page" : undefined}
                onPress={() =>
                  navigate(
                    lastLocations.current.get(session.key) ?? sessionRoute(session, "workflow"),
                  )
                }
              >
                {session.service?.name ?? "New service draft"}
              </Button>
            ))}
          </nav>
        )}
        {!route && (
          <section role="alert">
            <h1 id="location-error-title" tabIndex={-1}>
              Location unavailable
            </h1>
            <p>This location is invalid or belongs to another isolation scope.</p>
            <Button onPress={() => navigate({ isolationDomainId, view: "services" })}>
              View agent services
            </Button>
          </section>
        )}
        {(serviceReadLoading || (resolvingService && !serviceReadError)) && (
          <section aria-busy="true">
            <h1>Loading service</h1>
            <p>Reading the requested service.</p>
          </section>
        )}
        {serviceReadError && (
          <section role="alert">
            <h1 id="service-read-title" tabIndex={-1}>
              Service unavailable
            </h1>
            <p>{serviceReadError.message}</p>
            <Button onPress={() => route && navigate(route)}>Retry service read</Button>
          </section>
        )}
        {visitedSearch && (
          <div hidden={!showSearch} inert={!showSearch}>
            <ResourceSearch
              client={client}
              isolationDomainId={isolationDomainId}
              onOpen={(item) => {
                if (item.kind === "workspace")
                  navigate({
                    isolationDomainId,
                    view: "workspaces",
                    workspaceId: item.metadata.id,
                  });
                else if (item.kind === "agent-service")
                  navigate({
                    isolationDomainId,
                    view: "workflow",
                    serviceId: item.metadata.id,
                    serviceTab: "overview",
                  });
                else if (item.kind === "invocation")
                  navigate({
                    isolationDomainId,
                    view: "interactions",
                    serviceId: item.serviceId,
                    invocationId: item.metadata.id,
                  });
                else
                  navigate({
                    isolationDomainId,
                    view: "interactions",
                    serviceId: item.serviceId,
                    invocationId: item.invocationId,
                    runTab: "files",
                    artifactId: item.metadata.id,
                  });
              }}
            />
          </div>
        )}
        {visitedHome && (
          <div hidden={!showHome} inert={!showHome}>
            <WorkbenchHome
              client={client}
              isolationDomainId={isolationDomainId}
              recentWork={recentKeys.flatMap((key) => {
                const session = sessions.find((item) => item.key === key);
                return session
                  ? [
                      {
                        key,
                        name: session.service?.name ?? "New service draft",
                        kind: session.service
                          ? ("Agent service" as const)
                          : ("Unsaved service draft" as const),
                      },
                    ]
                  : [];
              })}
              onOpenWork={(key) => {
                const session = sessions.find((item) => item.key === key);
                if (session)
                  navigate(
                    lastLocations.current.get(session.key) ?? sessionRoute(session, "workflow"),
                  );
              }}
              onOpenRun={(run) =>
                navigate({
                  isolationDomainId,
                  serviceId: run.serviceId,
                  view: "interactions",
                  invocationId: run.metadata.id,
                })
              }
              onCreate={create}
              onBrowseRuns={() => navigateView("interactions")}
            />
          </div>
        )}
        {route && view === "services" && (
          <section aria-labelledby="services-title">
            <div className="workbench-page-heading">
              <div>
                <p className="workbench-kicker">Agents</p>
                <h1 id="services-title" tabIndex={-1}>
                  Agent services
                </h1>
                <p>
                  Create reusable agents, manage their revisions, and inspect their interactions.
                </p>
              </div>
              <Button onPress={create} variant="primary">
                New service
              </Button>
            </div>
            <ServiceDirectory
              services={servicePage.items}
              loading={servicePage.loading}
              error={servicePage.error}
              hasMore={servicePage.nextCursor !== undefined}
              query={serviceQuery}
              appliedQuery={appliedServiceQuery}
              onSearch={(query) => {
                if (query === appliedServiceQuery) void servicePage.load();
                else setAppliedServiceQuery(query);
              }}
              sort={serviceSort}
              onQueryChange={setServiceQuery}
              onSortChange={setServiceSort}
              onRefresh={() => void servicePage.load()}
              onLoadMore={() => void servicePage.load(servicePage.nextCursor)}
              onCreate={create}
              onOpen={(service) => {
                if (servicePage.canAct()) navigateToService(service);
              }}
              onHistory={(service) => {
                if (servicePage.canAct()) navigateToService(service, "interactions");
              }}
            />
          </section>
        )}
        {visitedWorkspaces && (
          <div hidden={!showWorkspaces} inert={!showWorkspaces}>
            <WorkspaceBrowser
              creationRequest={workspaceCreationRequest}
              client={client}
              isolationDomainId={isolationDomainId}
              workspaceId={lastWorkspaceId}
              active={showWorkspaces}
              onNavigate={(workspaceId) =>
                navigate({
                  isolationDomainId,
                  view: "workspaces",
                  ...(workspaceId ? { workspaceId } : {}),
                })
              }
              onOpenService={(serviceId) =>
                navigate({ isolationDomainId, view: "workflow", serviceId, serviceTab: "overview" })
              }
            />
          </div>
        )}
        {visitedArtifacts && (
          <div hidden={!showArtifacts} inert={!showArtifacts}>
            <ScopeArtifactBrowser
              client={client}
              isolationDomainId={isolationDomainId}
              onOpen={(artifact) =>
                navigate({
                  isolationDomainId,
                  view: "interactions",
                  serviceId: artifact.serviceId,
                  invocationId: artifact.invocationId,
                  runTab: "files",
                  artifactId: artifact.metadata.id,
                })
              }
            />
          </div>
        )}
        {visitedScopeRuns && (
          <div hidden={!showScopeRuns} inert={!showScopeRuns}>
            <ScopeRunBrowser
              client={client}
              isolationDomainId={isolationDomainId}
              onOpen={(run) =>
                navigate({
                  isolationDomainId,
                  serviceId: run.serviceId,
                  view: "interactions",
                  invocationId: run.metadata.id,
                })
              }
            />
          </div>
        )}
        {sessions.map((session) => {
          const active =
            !!route &&
            activeSession?.key === session.key &&
            !serviceReadLoading &&
            !serviceReadError;
          return (
            <div key={session.key} hidden={!active}>
              <ServiceWorkflowSession
                revisionLocation={active ? { revisionId: route?.revisionId } : undefined}
                onRevisionNavigate={(revisionId, tab) =>
                  navigate({
                    ...sessionRoute(session, "workflow", tab),
                    revisionId,
                  })
                }
                runLocation={
                  active
                    ? {
                        invocationId: route?.invocationId,
                        tab: route?.runTab,
                        artifactId: route?.artifactId,
                      }
                    : undefined
                }
                onRunNavigate={(location) =>
                  navigate({
                    isolationDomainId,
                    serviceId: session.service?.metadata.id,
                    view: "interactions",
                    invocationId: location.invocationId,
                    runTab: location.tab,
                    artifactId: location.artifactId,
                  })
                }
                selectedTab={
                  active
                    ? view === "interactions"
                      ? "runs"
                      : (route?.serviceTab ?? "build")
                    : undefined
                }
                client={client}
                isolationDomainId={isolationDomainId}
                onOpenWorkspace={
                  session.service?.workspaceId
                    ? () =>
                        navigate({
                          isolationDomainId,
                          view: "workspaces",
                          workspaceId: session.service?.workspaceId,
                        })
                    : undefined
                }
                initialService={session.service}
                active={active}
                view={active ? view : "services"}
                onNavigate={(next, tab) => navigate(sessionRoute(session, next, tab))}
                onServiceOpened={(service) => {
                  if (service.metadata.isolationDomainId !== isolationDomainId) return;
                  setSessions((current) =>
                    current.map((entry) =>
                      entry.key === session.key ? { ...entry, service } : entry,
                    ),
                  );
                  setLastSessionKey(session.key);
                  navigate({ isolationDomainId, serviceId: service.metadata.id, view: "workflow" });
                  void servicePage.load();
                }}
              />
            </div>
          );
        })}
      </div>
    </WorkbenchShell>
  );
}
