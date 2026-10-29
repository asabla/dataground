import { Button } from "@dataground/ui";
import { useCallback, useEffect, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import { ScopeRunBrowser } from "../invocations/ScopeRunBrowser";
import {
  type AgentServiceFailure,
  type AgentServiceResource,
  listAgentServices,
  readAgentService,
} from "../services/client";
import type { ServiceSort } from "../services/directory";
import { ServiceDirectory } from "../services/ServiceDirectory";
import type {
  ServiceTab,
  WorkbenchPresentation,
  WorkbenchRoute,
  WorkbenchView,
} from "./navigation";
import { ServiceWorkflowSession } from "./ServiceWorkflowSession";
import { useWorkbenchNavigation } from "./useWorkbenchNavigation";
import { WorkbenchShell } from "./WorkbenchShell";

export interface DevelopmentWorkbenchProps {
  presentation?: WorkbenchPresentation;
  client: DataGroundClient;
  isolationDomainId: string;
  onDisconnect: () => void;
}

export function DevelopmentWorkbench(props: DevelopmentWorkbenchProps) {
  const [connection, setConnection] = useState({
    client: props.client,
    scope: props.isolationDomainId,
    presentation: props.presentation,
    generation: 0,
  });
  if (
    connection.client !== props.client ||
    connection.scope !== props.isolationDomainId ||
    connection.presentation !== props.presentation
  ) {
    setConnection({
      client: props.client,
      scope: props.isolationDomainId,
      presentation: props.presentation,
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
  presentation = "workbench",
}: DevelopmentWorkbenchProps) {
  const [scopedServices, setScopedServices] = useState<AgentServiceResource[]>([]);
  const [serviceListError, setServiceListError] = useState<AgentServiceFailure>();
  const [serviceListLoading, setServiceListLoading] = useState(true);
  const [serviceListLoadingMore, setServiceListLoadingMore] = useState(false);
  const [serviceListNextCursor, setServiceListNextCursor] = useState<string>();
  const [serviceQuery, setServiceQuery] = useState("");
  const [serviceSort, setServiceSort] = useState<ServiceSort>("updated");
  const { route, navigate } = useWorkbenchNavigation(isolationDomainId);
  const [sessions, setSessions] = useState<OpenServiceWork[]>([]);
  const retainedSessions = useRef(sessions);
  retainedSessions.current = sessions;
  const nextDraft = useRef(0);
  const [lastSessionKey, setLastSessionKey] = useState<string>();
  const [serviceReadError, setServiceReadError] = useState<AgentServiceFailure>();
  const [serviceReadLoading, setServiceReadLoading] = useState(false);
  const serviceReadGeneration = useRef(0);
  const serviceListGeneration = useRef(0);
  const pageContent = useRef<HTMLDivElement>(null);
  const activeSession =
    route?.view === "services"
      ? undefined
      : route?.serviceId
        ? sessions.find((session) => session.service?.metadata.id === route.serviceId)
        : route?.view === "workflow"
          ? sessions.find((session) => !session.service)
          : undefined;
  const view = route?.view ?? "services";
  const showScopeRuns =
    !!route && view === "interactions" && !route.serviceId && presentation === "workbench";
  const [visitedScopeRuns, setVisitedScopeRuns] = useState(showScopeRuns);
  if (showScopeRuns && !visitedScopeRuns) setVisitedScopeRuns(true);
  const resolvingService = !!route?.serviceId && !activeSession;

  const loadServicePage = useCallback(
    async (cursor?: string) => {
      const generation = ++serviceListGeneration.current;
      setServiceListLoading(cursor === undefined);
      setServiceListLoadingMore(cursor !== undefined);
      setServiceListError(undefined);
      const result = await listAgentServices(client, isolationDomainId, cursor);
      if (generation !== serviceListGeneration.current) return;
      setServiceListLoading(false);
      setServiceListLoadingMore(false);
      if (!result.ok) {
        setServiceListError(result.error);
        return;
      }
      setServiceListNextCursor(result.page.nextCursor);
      setScopedServices((current) => {
        if (cursor === undefined) return result.page.items;
        const merged = [...current];
        const known = new Set(current.map((service) => service.metadata.id));
        for (const service of result.page.items) {
          if (!known.has(service.metadata.id)) merged.push(service);
        }
        return merged;
      });
    },
    [client, isolationDomainId],
  );

  useEffect(() => {
    void loadServicePage();
    return () => {
      serviceListGeneration.current++;
    };
  }, [loadServicePage]);

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
    if (!route || route.view === "services") return;
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
    const id = !route
      ? "location-error-title"
      : serviceReadError
        ? "service-read-title"
        : view === "services"
          ? "services-title"
          : showScopeRuns
            ? "scope-runs-title"
            : undefined;
    if (id) pageContent.current?.querySelector<HTMLElement>(`#${id}`)?.focus();
  }, [route, resolvingService, serviceReadLoading, serviceReadError, view, showScopeRuns]);

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
      ...(presentation === "workbench" && next === "workflow"
        ? { serviceTab: "overview" as const }
        : {}),
    });
  };
  const navigateView = (next: WorkbenchView) => {
    const context = activeSession ?? sessions.find((session) => session.key === lastSessionKey);
    navigate({
      isolationDomainId,
      view: next,
      ...(next === "services" || (next === "interactions" && presentation === "workbench")
        ? {}
        : { serviceId: context?.service?.metadata.id }),
    });
  };
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
      scopeRuns={presentation === "workbench"}
    >
      <div className="workbench-main" ref={pageContent}>
        {sessions.length > 0 && (
          <nav aria-label="Open service work" className="workbench-open-work">
            {sessions.map((session) => (
              <Button
                key={session.key}
                variant="quiet"
                aria-current={activeSession?.key === session.key ? "page" : undefined}
                onPress={() => navigate(sessionRoute(session, "workflow"))}
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
              services={scopedServices}
              loading={serviceListLoading}
              loadingMore={serviceListLoadingMore}
              error={serviceListError}
              hasMore={serviceListNextCursor !== undefined}
              query={serviceQuery}
              sort={serviceSort}
              onQueryChange={setServiceQuery}
              onSortChange={setServiceSort}
              onRefresh={() => void loadServicePage()}
              onLoadMore={() => void loadServicePage(serviceListNextCursor)}
              onCreate={create}
              onOpen={(service) => navigateToService(service)}
              onHistory={(service) => navigateToService(service, "interactions")}
            />
          </section>
        )}
        {route && view === "interactions" && !route.serviceId && presentation === "classic" && (
          <section aria-labelledby="interactions-title">
            <h1 id="interactions-title" tabIndex={-1}>
              Interactions
            </h1>
            <p>Select a service to browse its invocation history.</p>
            <Button onPress={() => navigateView("services")}>View agent services</Button>
          </section>
        )}
        {visitedScopeRuns && presentation === "workbench" && (
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
                presentation={presentation}
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
                  setScopedServices((current) => [
                    service,
                    ...current.filter((entry) => entry.metadata.id !== service.metadata.id),
                  ]);
                  navigate({ isolationDomainId, serviceId: service.metadata.id, view: "workflow" });
                  void loadServicePage();
                }}
              />
            </div>
          );
        })}
      </div>
    </WorkbenchShell>
  );
}
