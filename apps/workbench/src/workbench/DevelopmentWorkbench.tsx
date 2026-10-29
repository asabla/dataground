import { Button } from "@dataground/ui";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  isServiceAliasRoutedToRevision,
  readServiceAlias,
  type ServiceAliasFailure,
  type ServiceAliasResource,
} from "../aliases";
import { ServiceAliasWithdrawWorkflow } from "../aliases/ServiceAliasWithdrawWorkflow";
import { ServiceRouteDiscovery } from "../aliases/ServiceRouteDiscovery";
import type { InvocationApprovalReference } from "../approvals";
import type { InvocationArtifactReference } from "../artifacts";
import type { DataGroundClient } from "../contracts/client";
import { InvocationHistoryWorkflow } from "../invocations/InvocationHistoryWorkflow";
import type { InvocationQuestionReference } from "../questions";
import {
  listServiceRevisions,
  type PublishedServiceRevisionResource,
  readServiceRevision,
  resumeServiceRevision,
  type ServiceRevisionFailure,
  type ServiceRevisionHistoryResource,
  ServiceRevisionHistoryWorkflow,
  type ServiceRevisionResource,
} from "../revisions";
import { ServiceRevisionRetireWorkflow } from "../revisions/ServiceRevisionRetireWorkflow";
import {
  AgentServiceAuthoringWorkflow,
  type AgentServiceFailure,
  type AgentServiceInvocationSelection,
  type AgentServiceResource,
  listAgentServices,
  readAgentService,
} from "../services";
import type { ServiceSort } from "../services/directory";
import { ServiceDirectory } from "../services/ServiceDirectory";
import type { WorkbenchView } from "./navigation";
import { useWorkbenchNavigation } from "./useWorkbenchNavigation";
import { WorkbenchShell } from "./WorkbenchShell";

type WorkflowStageIndex = 0 | 1 | 2 | 3 | 4 | 5;

const workflowSteps = [
  { label: "Purpose", shortLabel: "Create service" },
  { label: "Runtime & access", shortLabel: "Create revision" },
  { label: "Review", shortLabel: "Publish revision" },
  { label: "Route", shortLabel: "Assign alias" },
  { label: "Run", shortLabel: "Start interaction" },
  { label: "Observe", shortLabel: "Monitor interaction" },
] as const;

function stageIndex(
  service?: AgentServiceResource,
  revision?: ServiceRevisionResource,
  publication?: PublishedServiceRevisionResource,
  alias?: ServiceAliasResource,
  invocation?: AgentServiceInvocationSelection,
): WorkflowStageIndex {
  if (invocation) return 5;
  if (alias) return 4;
  if (publication) return 3;
  if (revision) return 2;
  if (service) return 1;
  return 0;
}

function historyFromDraft(revision: ServiceRevisionResource): ServiceRevisionHistoryResource {
  return {
    ...revision,
    metadata: { ...revision.metadata },
    requiredCapabilities: [...revision.requiredCapabilities],
  };
}

function mergeRevisionHistory(
  current: ServiceRevisionHistoryResource[],
  incoming: ServiceRevisionHistoryResource[],
) {
  const byID = new Map(current.map((revision) => [revision.metadata.id, revision]));
  for (const revision of incoming) byID.set(revision.metadata.id, revision);
  return [...byID.values()].sort(
    (left, right) =>
      right.revisionNumber - left.revisionNumber ||
      right.metadata.id.localeCompare(left.metadata.id),
  );
}

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

export function WorkbenchSession({
  client,
  isolationDomainId,
  onDisconnect,
}: DevelopmentWorkbenchProps) {
  const [draftFormGeneration, setDraftFormGeneration] = useState(0);
  const [observedAlias, setObservedAlias] = useState<ServiceAliasResource>();
  const [openedAlias, setOpenedAlias] = useState<ServiceAliasResource>();
  const [openedQuestion, setOpenedQuestion] = useState<InvocationQuestionReference>();
  const [openedApproval, setOpenedApproval] = useState<InvocationApprovalReference>();
  const [openedArtifact, setOpenedArtifact] = useState<InvocationArtifactReference>();
  const [openedInvocation, setOpenedInvocation] = useState<AgentServiceInvocationSelection>();
  const [openedPublishedRevision, setOpenedPublishedRevision] =
    useState<PublishedServiceRevisionResource>();
  const [openedRevision, setOpenedRevision] = useState<ServiceRevisionResource>();
  const [withdrawalAlias, setWithdrawalAlias] = useState<ServiceAliasResource>();
  const [retirementRevision, setRetirementRevision] = useState<ServiceRevisionHistoryResource>();
  const [openedService, setOpenedService] = useState<AgentServiceResource>();
  const [revisionHistory, setRevisionHistory] = useState<ServiceRevisionHistoryResource[]>([]);
  const [aliasReadError, setAliasReadError] = useState<ServiceAliasFailure>();
  const [aliasReadLoading, setAliasReadLoading] = useState(false);
  const [aliasReadTarget, setAliasReadTarget] = useState<{
    name: string;
    required: boolean;
    revisionId?: string;
  }>({ name: "stable", required: false });
  const [revisionListError, setRevisionListError] = useState<ServiceRevisionFailure>();
  const [revisionListLoading, setRevisionListLoading] = useState(false);
  const [revisionListLoadingMore, setRevisionListLoadingMore] = useState(false);
  const [revisionListNextCursor, setRevisionListNextCursor] = useState<string>();
  const [scopedServices, setScopedServices] = useState<AgentServiceResource[]>([]);
  const [serviceListError, setServiceListError] = useState<AgentServiceFailure>();
  const [serviceListLoading, setServiceListLoading] = useState(true);
  const [serviceListLoadingMore, setServiceListLoadingMore] = useState(false);
  const [serviceListNextCursor, setServiceListNextCursor] = useState<string>();
  const [serviceQuery, setServiceQuery] = useState("");
  const [serviceSort, setServiceSort] = useState<ServiceSort>("updated");
  const pageContent = useRef<HTMLDivElement>(null);
  const { route, navigate } = useWorkbenchNavigation(isolationDomainId);
  const view = route?.view ?? "services";
  const resolvingService =
    route?.serviceId !== undefined && route.serviceId !== openedService?.metadata.id;
  const [workflowVisited, setWorkflowVisited] = useState(false);
  const [historyVisited, setHistoryVisited] = useState(false);
  const [serviceReadError, setServiceReadError] = useState<AgentServiceFailure>();
  const [serviceReadLoading, setServiceReadLoading] = useState(false);
  const retainedService = useRef(openedService);
  retainedService.current = openedService;
  const serviceReadGeneration = useRef(0);
  const setView = (next: WorkbenchView) =>
    navigate({
      isolationDomainId,
      view: next,
      ...(next === "services" ? {} : { serviceId: openedService?.metadata.id }),
    });
  useEffect(() => {
    if (view === "workflow") setWorkflowVisited(true);
    if (view === "interactions") setHistoryVisited(true);
  }, [view]);
  useEffect(() => {
    if (resolvingService || serviceReadLoading) return;
    const heading = !route
      ? "location-error-title"
      : serviceReadError
        ? "service-read-title"
        : `${route.view}-title`;
    pageContent.current?.querySelector<HTMLElement>(`#${heading}`)?.focus();
  }, [route, resolvingService, serviceReadLoading, serviceReadError]);
  const aliasReadGeneration = useRef(0);
  const revisionListGeneration = useRef(0);
  const serviceListGeneration = useRef(0);

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

  const loadObservedAlias = useCallback(
    async (service: AgentServiceResource, name = "stable", required = false) => {
      const generation = ++aliasReadGeneration.current;
      setAliasReadTarget({ name, required });
      setAliasReadLoading(true);
      setAliasReadError(undefined);
      setObservedAlias(undefined);
      setOpenedAlias(undefined);
      const result = await readServiceAlias(
        client,
        {
          isolationDomainId: service.metadata.isolationDomainId,
          serviceId: service.metadata.id,
        },
        name,
      );
      if (generation !== aliasReadGeneration.current) return;
      if (!result.ok) {
        setAliasReadLoading(false);
        setAliasReadError(result.error);
        return;
      }
      if (!result.alias) {
        setAliasReadLoading(false);
        if (required)
          setAliasReadError({
            code: "WORKBENCH_ROUTE_NOT_FOUND",
            message:
              "This route is no longer active. Refresh service routes to choose another route.",
            retryable: false,
          });
        return;
      }
      const exact = await readServiceRevision(client, {
        isolationDomainId: service.metadata.isolationDomainId,
        serviceId: service.metadata.id,
        revisionId: result.alias.revisionId,
      });
      if (generation !== aliasReadGeneration.current) return;
      setAliasReadLoading(false);
      if (!exact.ok) {
        setAliasReadError(exact.error);
        return;
      }
      const selection = resumeServiceRevision(exact.revision);
      if (
        !selection.publishedRevision ||
        !isServiceAliasRoutedToRevision(result.alias, selection.publishedRevision)
      ) {
        setAliasReadError({
          code: "WORKBENCH_ROUTE_REVISION_UNAVAILABLE",
          message:
            "The route does not resolve to a published revision. Refresh service routes before continuing.",
          retryable: false,
        });
        return;
      }
      setOpenedRevision(selection.revision);
      setOpenedPublishedRevision(selection.publishedRevision);
      setRevisionHistory((current) => mergeRevisionHistory(current, [exact.revision]));
      setObservedAlias(result.alias);
      setOpenedAlias(result.alias);
    },
    [client],
  );

  const openServiceRevision = useCallback(
    async (service: AgentServiceResource, revisionId: string) => {
      const generation = ++aliasReadGeneration.current;
      setAliasReadTarget({ name: "stable", required: false, revisionId });
      setAliasReadLoading(true);
      setAliasReadError(undefined);
      setObservedAlias(undefined);
      setOpenedAlias(undefined);
      setOpenedQuestion(undefined);
      setOpenedApproval(undefined);
      setOpenedArtifact(undefined);
      setOpenedInvocation(undefined);
      setOpenedPublishedRevision(undefined);
      setOpenedRevision(undefined);
      const exact = await readServiceRevision(client, {
        isolationDomainId: service.metadata.isolationDomainId,
        serviceId: service.metadata.id,
        revisionId,
      });
      if (generation !== aliasReadGeneration.current) return;
      if (!exact.ok) {
        setAliasReadLoading(false);
        setAliasReadError(exact.error);
        return;
      }
      const selection = resumeServiceRevision(exact.revision);
      if (!selection.revision) {
        setAliasReadLoading(false);
        setAliasReadError({
          code: "WORKBENCH_REVISION_RETIRED",
          message:
            "This revision has retired. Create a new revision to change the service definition.",
          retryable: false,
        });
        setRevisionHistory((current) => mergeRevisionHistory(current, [exact.revision]));
        return;
      }
      // Revision management keeps the chosen publication as the routing target.
      // The current alias is only an observed version precondition for assignment.
      if (selection.publishedRevision) {
        const route = await readServiceAlias(
          client,
          { isolationDomainId: service.metadata.isolationDomainId, serviceId: service.metadata.id },
          "stable",
        );
        if (generation !== aliasReadGeneration.current) return;
        if (!route.ok) {
          setAliasReadLoading(false);
          setAliasReadError(route.error);
          return;
        }
        setObservedAlias(route.alias);
      }
      setAliasReadLoading(false);
      setOpenedRevision(selection.revision);
      setOpenedPublishedRevision(selection.publishedRevision);
      setRevisionHistory((current) => mergeRevisionHistory(current, [exact.revision]));
    },
    [client],
  );

  const loadRevisionPage = useCallback(
    async (service: AgentServiceResource, cursor?: string) => {
      const generation = ++revisionListGeneration.current;
      if (cursor === undefined) {
        setRevisionListLoading(true);
        setRevisionHistory([]);
        setRevisionListNextCursor(undefined);
      } else {
        setRevisionListLoadingMore(true);
      }
      setRevisionListError(undefined);
      const result = await listServiceRevisions(
        client,
        isolationDomainId,
        service.metadata.id,
        cursor,
      );
      if (generation !== revisionListGeneration.current) return;
      setRevisionListLoading(false);
      setRevisionListLoadingMore(false);
      if (!result.ok) {
        setRevisionListError(result.error);
        return;
      }
      setRevisionListNextCursor(result.page.nextCursor);
      setRevisionHistory((current) =>
        cursor === undefined ? result.page.items : mergeRevisionHistory(current, result.page.items),
      );
      if (cursor === undefined) {
        const newest = result.page.items[0];
        const selection = newest === undefined ? {} : resumeServiceRevision(newest);
        setOpenedPublishedRevision(selection.publishedRevision);
        setOpenedRevision(selection.revision);
        void loadObservedAlias(service);
      }
    },
    [client, isolationDomainId, loadObservedAlias],
  );

  useEffect(
    () => () => {
      aliasReadGeneration.current++;
      revisionListGeneration.current++;
    },
    [],
  );

  const currentStage = stageIndex(
    openedService,
    openedRevision,
    openedPublishedRevision,
    openedAlias,
    openedInvocation,
  );
  const clearWorkflow = useCallback(() => {
    aliasReadGeneration.current++;
    revisionListGeneration.current++;
    setObservedAlias(undefined);
    setOpenedAlias(undefined);
    setOpenedQuestion(undefined);
    setOpenedApproval(undefined);
    setOpenedArtifact(undefined);
    setOpenedInvocation(undefined);
    setOpenedPublishedRevision(undefined);
    setOpenedRevision(undefined);
    setOpenedService(undefined);
    setRetirementRevision(undefined);
    setWithdrawalAlias(undefined);
    setRevisionHistory([]);
    setAliasReadError(undefined);
    setAliasReadLoading(false);
    setRevisionListError(undefined);
    setRevisionListLoading(false);
    setRevisionListLoadingMore(false);
    setRevisionListNextCursor(undefined);
  }, []);

  const openService = (service: AgentServiceResource, reset = false) => {
    if (!reset && openedService?.metadata.id === service.metadata.id) {
      navigate({ isolationDomainId, serviceId: service.metadata.id, view: "workflow" });
      return;
    }
    aliasReadGeneration.current++;
    revisionListGeneration.current++;
    setObservedAlias(undefined);
    setOpenedAlias(undefined);
    setOpenedQuestion(undefined);
    setOpenedApproval(undefined);
    setOpenedArtifact(undefined);
    setOpenedInvocation(undefined);
    setOpenedPublishedRevision(undefined);
    setOpenedRevision(undefined);
    setOpenedService(service);
    setRetirementRevision(undefined);
    setWithdrawalAlias(undefined);
    setRevisionHistory([]);
    setAliasReadError(undefined);
    setAliasReadLoading(false);
    setRevisionListError(undefined);
    setRevisionListNextCursor(undefined);
    navigate({ isolationDomainId, serviceId: service.metadata.id, view: "workflow" });
    void loadRevisionPage(service);
  };

  useEffect(() => {
    const generation = ++serviceReadGeneration.current;
    setServiceReadError(undefined);
    setServiceReadLoading(false);
    if (!route) return;
    if (route.view === "workflow" && !route.serviceId) {
      if (retainedService.current) clearWorkflow();
      return;
    }
    if (!route.serviceId || route.serviceId === retainedService.current?.metadata.id) return;
    clearWorkflow();
    setServiceReadLoading(true);
    void readAgentService(client, isolationDomainId, route.serviceId).then((result) => {
      if (generation !== serviceReadGeneration.current) return;
      setServiceReadLoading(false);
      if (!result.ok) {
        setServiceReadError(result.error);
        return;
      }
      setOpenedService(result.service);
      void loadRevisionPage(result.service);
    });
    return () => {
      serviceReadGeneration.current++;
    };
  }, [client, isolationDomainId, route, clearWorkflow, loadRevisionPage]);

  const startServiceRevision = () => {
    setDraftFormGeneration((current) => current + 1);
    aliasReadGeneration.current++;
    setAliasReadLoading(false);
    setAliasReadError(undefined);
    setObservedAlias(undefined);
    setOpenedAlias(undefined);
    setOpenedQuestion(undefined);
    setOpenedApproval(undefined);
    setOpenedArtifact(undefined);
    setOpenedInvocation(undefined);
    setOpenedPublishedRevision(undefined);
    setOpenedRevision(undefined);
    setRetirementRevision(undefined);
    setWithdrawalAlias(undefined);
  };

  const workflow = (
    <AgentServiceAuthoringWorkflow
      key={draftFormGeneration}
      canAssignAlias
      canCancelInvocation
      canCreateRevision
      canCreateService
      canPublishRevision
      canAnswerQuestion
      canResolveApproval
      canInvokeService
      client={client}
      focusCurrentStage
      isolationDomainId={isolationDomainId}
      onAssignAlias={(revision) => {
        if (openedService) void openServiceRevision(openedService, revision.metadata.id);
      }}
      onComposeInvocation={(alias) => {
        setObservedAlias(alias);
        setOpenedAlias(alias);
        setOpenedQuestion(undefined);
        setOpenedApproval(undefined);
        setOpenedArtifact(undefined);
        setOpenedInvocation(undefined);
      }}
      onCloseQuestion={() => setOpenedQuestion(undefined)}
      onInspectQuestion={(reference) => {
        setOpenedApproval(undefined);
        setOpenedArtifact(undefined);
        setOpenedQuestion(reference);
      }}
      onCloseApproval={() => setOpenedApproval(undefined)}
      onCloseArtifact={() => setOpenedArtifact(undefined)}
      onInspectApproval={(reference) => {
        setOpenedQuestion(undefined);
        setOpenedArtifact(undefined);
        setOpenedApproval(reference);
      }}
      onInspectArtifact={(reference) => {
        setOpenedQuestion(undefined);
        setOpenedApproval(undefined);
        setOpenedArtifact(reference);
      }}
      onOpenInvocation={(selection) => {
        setOpenedQuestion(undefined);
        setOpenedApproval(undefined);
        setOpenedArtifact(undefined);
        setOpenedInvocation(selection);
        setView("interactions");
      }}
      onOpenRevision={(revision) => {
        aliasReadGeneration.current++;
        setAliasReadLoading(false);
        setAliasReadError(undefined);
        revisionListGeneration.current++;
        setOpenedAlias(undefined);
        setOpenedQuestion(undefined);
        setOpenedApproval(undefined);
        setOpenedArtifact(undefined);
        setOpenedInvocation(undefined);
        setOpenedPublishedRevision(undefined);
        setOpenedRevision(revision);
        setRevisionHistory((current) =>
          mergeRevisionHistory(current, [historyFromDraft(revision)]),
        );
      }}
      onOpenService={(service) => {
        setScopedServices((current) => {
          const existing = current.findIndex(
            (candidate) => candidate.metadata.id === service.metadata.id,
          );
          if (existing === -1) return [service, ...current];
          return current.map((candidate, index) => (index === existing ? service : candidate));
        });
        openService(service);
        void loadServicePage();
      }}
      onRevisionPublished={(revision) => {
        if (
          revision.metadata.isolationDomainId !== isolationDomainId ||
          revision.serviceId !== openedService?.metadata.id ||
          revision.metadata.id !== openedRevision?.metadata.id
        )
          return;
        revisionListGeneration.current++;
        setRevisionListLoading(false);
        setRevisionListLoadingMore(false);
        setRevisionHistory((current) => mergeRevisionHistory(current, [revision]));
      }}
      observedAlias={observedAlias}
      selectedAlias={openedAlias}
      selectedQuestion={openedQuestion}
      selectedApproval={openedApproval}
      selectedArtifact={openedArtifact}
      selectedInvocation={openedInvocation}
      selectedPublishedRevision={openedPublishedRevision}
      selectedRevision={openedRevision}
      selectedService={openedService}
    />
  );

  return (
    <WorkbenchShell
      isolationDomainId={isolationDomainId}
      onDisconnect={onDisconnect}
      view={view}
      onNavigate={setView}
    >
      <div className="workbench-main" ref={pageContent}>
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
                <p className="workbench-kicker">AI workspace</p>
                <h1 id="services-title" tabIndex={-1}>
                  Agent services
                </h1>
                <p>
                  Create reusable agents, manage their revisions, and inspect their interactions.
                </p>
              </div>
              <Button
                onPress={() => {
                  clearWorkflow();
                  navigate({ isolationDomainId, view: "workflow" });
                }}
                variant="primary"
              >
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
              onCreate={() => {
                clearWorkflow();
                navigate({ isolationDomainId, view: "workflow" });
              }}
              onOpen={openService}
              onHistory={(service) => {
                navigate({
                  isolationDomainId,
                  serviceId: service.metadata.id,
                  view: "interactions",
                });
              }}
            />
          </section>
        )}

        {(workflowVisited || view === "workflow") && (
          <section
            aria-labelledby="workflow-title"
            hidden={
              !route ||
              resolvingService ||
              serviceReadLoading ||
              !!serviceReadError ||
              view !== "workflow"
            }
          >
            <div className="workbench-page-heading workbench-page-heading--compact">
              <div>
                <p className="workbench-breadcrumb">
                  Agent services / {openedService?.name ?? "New service"}
                </p>
                <h1 id="workflow-title" tabIndex={-1}>
                  {openedService?.name ?? "Create an agent service"}
                </h1>
                <p>{workflowSteps[currentStage].shortLabel} to continue the governed setup.</p>
              </div>
              <Button onPress={() => setView("services")} variant="quiet">
                Back to services
              </Button>
            </div>

            <div>
              <div className="workbench-stage">
                {openedService && (
                  <ServiceRevisionHistoryWorkflow
                    client={client}
                    isolationDomainId={isolationDomainId}
                    serviceId={openedService.metadata.id}
                    error={revisionListError}
                    isLoading={revisionListLoading}
                    isLoadingMore={revisionListLoadingMore}
                    nextCursor={revisionListNextCursor}
                    onLoadMore={() =>
                      revisionListNextCursor &&
                      void loadRevisionPage(openedService, revisionListNextCursor)
                    }
                    onRetry={() => void loadRevisionPage(openedService)}
                    onCreate={
                      retirementRevision || withdrawalAlias ? undefined : startServiceRevision
                    }
                    onOpen={
                      retirementRevision || withdrawalAlias
                        ? undefined
                        : (revision) =>
                            void openServiceRevision(openedService, revision.metadata.id)
                    }
                    onRetire={
                      retirementRevision || withdrawalAlias
                        ? undefined
                        : (revision) => {
                            aliasReadGeneration.current++;
                            setAliasReadLoading(false);
                            setAliasReadError(undefined);
                            setRetirementRevision(revision);
                          }
                    }
                    revisions={revisionHistory}
                  />
                )}
                {openedService && !retirementRevision && !withdrawalAlias && (
                  <ServiceRouteDiscovery
                    client={client}
                    scope={{ isolationDomainId, serviceId: openedService.metadata.id }}
                    canWithdraw
                    onWithdraw={(alias) => {
                      aliasReadGeneration.current++;
                      setAliasReadLoading(false);
                      setAliasReadError(undefined);
                      setWithdrawalAlias(alias);
                    }}
                    onSelect={
                      revisionListLoading || revisionListError
                        ? undefined
                        : (alias) => {
                            setOpenedQuestion(undefined);
                            setOpenedApproval(undefined);
                            setOpenedArtifact(undefined);
                            setOpenedInvocation(undefined);
                            void loadObservedAlias(openedService, alias.name, true);
                          }
                    }
                  />
                )}
                {withdrawalAlias &&
                openedService &&
                withdrawalAlias.metadata.isolationDomainId === isolationDomainId &&
                withdrawalAlias.serviceId === openedService.metadata.id ? (
                  <ServiceAliasWithdrawWorkflow
                    alias={withdrawalAlias}
                    canWithdraw
                    client={client}
                    onClose={() => {
                      openService(openedService, true);
                      document.getElementById("revision-history-title")?.focus();
                    }}
                    onWithdrawn={() => {
                      aliasReadGeneration.current++;
                      setAliasReadLoading(false);
                      setAliasReadError(undefined);
                      setObservedAlias(undefined);
                      setOpenedAlias(undefined);
                    }}
                  />
                ) : null}
                {retirementRevision &&
                  openedService &&
                  retirementRevision.metadata.isolationDomainId === isolationDomainId &&
                  retirementRevision.serviceId === openedService.metadata.id && (
                    <ServiceRevisionRetireWorkflow
                      key={`${retirementRevision.metadata.id}/${retirementRevision.metadata.version}`}
                      client={client}
                      revision={retirementRevision}
                      canRetire
                      onClose={() => {
                        openService(openedService, true);
                        document.getElementById("revision-history-title")?.focus();
                      }}
                      onRetired={(revision) =>
                        setRevisionHistory((current) => mergeRevisionHistory(current, [revision]))
                      }
                    />
                  )}
                {openedService && aliasReadLoading && (
                  <section aria-busy="true" className="workbench-empty">
                    <span aria-hidden="true" className="workbench-empty__mark">
                      ◇
                    </span>
                    <h2>
                      {aliasReadTarget.revisionId
                        ? "Loading service revision"
                        : `Loading ${aliasReadTarget.name} route`}
                    </h2>
                    <p>Reading the exact alias state before routing or invoking this service.</p>
                  </section>
                )}
                {openedService && aliasReadError && (
                  <section className="workbench-inline-error" role="alert">
                    <p>{aliasReadError.message}</p>
                    <Button
                      onPress={() =>
                        aliasReadTarget.revisionId
                          ? void openServiceRevision(openedService, aliasReadTarget.revisionId)
                          : void loadObservedAlias(
                              openedService,
                              aliasReadTarget.name,
                              aliasReadTarget.required,
                            )
                      }
                      variant="quiet"
                    >
                      Retry route discovery
                    </Button>
                  </section>
                )}
              </div>
            </div>
          </section>
        )}

        {(historyVisited || view === "interactions") && (
          <section
            aria-labelledby="interactions-title"
            hidden={
              !route ||
              resolvingService ||
              serviceReadLoading ||
              !!serviceReadError ||
              view !== "interactions"
            }
          >
            <div className="workbench-page-heading workbench-page-heading--compact">
              <div>
                <p className="workbench-breadcrumb">
                  {openedService ? `${openedService.name} / Interactions` : "AI workspace"}
                </p>
                <h1 id="interactions-title" tabIndex={-1}>
                  {openedInvocation ? "Interaction details" : "Interactions"}
                </h1>
                <p>
                  {openedInvocation
                    ? "Observe lifecycle state and replay the ordered event record."
                    : "Open an invocation to inspect its lifecycle, events, approvals, and artifacts."}
                </p>
              </div>
              {openedInvocation && (
                <Button
                  onPress={() => {
                    setOpenedInvocation(undefined);
                    setOpenedQuestion(undefined);
                    setOpenedApproval(undefined);
                    setOpenedArtifact(undefined);
                  }}
                  variant="quiet"
                >
                  View invocation history
                </Button>
              )}
            </div>
            {openedService && (
              <div className="workbench-context-actions">
                <Button onPress={() => openService(openedService)} variant="quiet">
                  Manage service
                </Button>
                <Button onPress={() => setView("services")} variant="quiet">
                  Change service
                </Button>
              </div>
            )}
            {!openedInvocation && openedService ? (
              <InvocationHistoryWorkflow
                client={client}
                target={{ isolationDomainId, serviceId: openedService.metadata.id }}
              />
            ) : !openedInvocation && !openedService ? (
              <section className="workbench-empty" aria-labelledby="empty-interactions-title">
                <span aria-hidden="true" className="workbench-empty__mark">
                  ↗
                </span>
                <h2 id="empty-interactions-title">No interaction is open</h2>
                <p>Select a service to browse its invocation history or start a new interaction.</p>
                <Button onPress={() => setView(openedService ? "workflow" : "services")}>
                  {openedService ? "Continue service setup" : "View agent services"}
                </Button>
              </section>
            ) : null}
          </section>
        )}
        {(workflowVisited || view === "workflow" || openedInvocation) && (
          <div
            hidden={
              !route ||
              resolvingService ||
              serviceReadLoading ||
              !!serviceReadError ||
              (view !== "workflow" && !(view === "interactions" && openedInvocation))
            }
            className="workbench-command-region"
          >
            {openedService &&
            revisionHistory.length === 0 &&
            (revisionListLoading || revisionListError)
              ? null
              : retirementRevision || withdrawalAlias
                ? null
                : openedService && (aliasReadLoading || aliasReadError)
                  ? null
                  : workflow}
          </div>
        )}
      </div>
    </WorkbenchShell>
  );
}
