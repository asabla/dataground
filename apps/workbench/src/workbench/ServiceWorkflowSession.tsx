import { Button } from "@dataground/ui";
import { useCallback, useEffect, useId, useRef, useState } from "react";
import {
  isServiceAliasRoutedToRevision,
  readServiceAlias,
  type ServiceAliasFailure,
  type ServiceAliasResource,
} from "../aliases";
import { ServiceAliasWithdrawWorkflow } from "../aliases/ServiceAliasWithdrawWorkflow";
import { ServiceRouteDiscovery } from "../aliases/ServiceRouteDiscovery";
import type { DataGroundClient } from "../contracts/client";
import type { RunLocation } from "../invocations/navigation";
import {
  listServiceRevisions,
  type PublishedServiceRevisionResource,
  readServiceRevision,
  resumeServiceRevision,
  ServiceRevisionDraftWorkflow,
  type ServiceRevisionFailure,
  type ServiceRevisionHistoryResource,
  ServiceRevisionHistoryWorkflow,
  type ServiceRevisionResource,
} from "../revisions";
import { ServiceRevisionRetireWorkflow } from "../revisions/ServiceRevisionRetireWorkflow";
import { AgentServiceCreateWorkflow, type AgentServiceResource } from "../services";
import type { ServiceTab, WorkbenchView } from "./navigation";

import { RetainedServiceCommands } from "./RetainedServiceCommands";
import { ServiceWorkbenchView } from "./ServiceWorkbenchView";

type WorkflowStageIndex = 0 | 1 | 2 | 3 | 4;

const workflowSteps = [
  { label: "Purpose", shortLabel: "Create service" },
  { label: "Runtime & access", shortLabel: "Create revision" },
  { label: "Review", shortLabel: "Publish revision" },
  { label: "Route", shortLabel: "Assign alias" },
  { label: "Run", shortLabel: "Start interaction" },
] as const;

function stageIndex(
  service?: AgentServiceResource,
  revision?: ServiceRevisionResource,
  publication?: PublishedServiceRevisionResource,
  alias?: ServiceAliasResource,
): WorkflowStageIndex {
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
  for (const revision of incoming) {
    const previous = byID.get(revision.metadata.id);
    if (!previous || revision.metadata.version >= previous.metadata.version)
      byID.set(revision.metadata.id, revision);
  }
  return [...byID.values()].sort(
    (left, right) =>
      right.revisionNumber - left.revisionNumber ||
      right.metadata.id.localeCompare(left.metadata.id),
  );
}

interface ServiceWorkflowSessionProps {
  selectedTab?: ServiceTab;
  revisionLocation?: { revisionId?: string };
  onRevisionNavigate: (revisionId: string | undefined, tab: ServiceTab) => void;
  runLocation?: RunLocation;
  onRunNavigate: (location: RunLocation) => void;
  client: DataGroundClient;
  isolationDomainId: string;
  initialService?: AgentServiceResource;
  active: boolean;
  view: WorkbenchView;
  onNavigate: (view: WorkbenchView, tab?: ServiceTab) => void;
  onOpenWorkspace?: () => void;
  onServiceOpened: (service: AgentServiceResource) => void;
}

export function ServiceWorkflowSession({
  client,
  isolationDomainId,
  selectedTab,
  revisionLocation,
  onRevisionNavigate,
  runLocation,
  onRunNavigate,
  initialService,
  active,
  view,
  onNavigate,
  onOpenWorkspace,
  onServiceOpened,
}: ServiceWorkflowSessionProps) {
  const [runRefreshGeneration, setRunRefreshGeneration] = useState(0);
  const [routeRefreshGeneration, setRouteRefreshGeneration] = useState(0);
  const [retainedTab, setRetainedTab] = useState<ServiceTab>("build");
  const tab = selectedTab ?? retainedTab;
  useEffect(() => {
    if (selectedTab) setRetainedTab(selectedTab);
  }, [selectedTab]);
  const selectTab = (next: ServiceTab) =>
    onNavigate(next === "runs" ? "interactions" : "workflow", next);
  const headingPrefix = useId();
  const revisionHistoryTitle = `${headingPrefix}-revisions`;
  const [initialResource] = useState(initialService);
  const [draftFormGeneration, setDraftFormGeneration] = useState(0);
  const [draftCompleted, setDraftCompleted] = useState(false);
  const [observedAlias, setObservedAlias] = useState<ServiceAliasResource>();
  const [openedAlias, setOpenedAlias] = useState<ServiceAliasResource>();
  const [openedPublishedRevision, setOpenedPublishedRevision] =
    useState<PublishedServiceRevisionResource>();
  const [openedRevision, setOpenedRevision] = useState<ServiceRevisionResource>();
  const [withdrawalAlias, setWithdrawalAlias] = useState<ServiceAliasResource>();
  const [retirementRevision, setRetirementRevision] = useState<ServiceRevisionHistoryResource>();
  const [openedService, setOpenedService] = useState<AgentServiceResource | undefined>(
    initialService,
  );
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
  const pageContent = useRef<HTMLDivElement>(null);
  const wasActive = useRef(false);
  useEffect(() => {
    if (active && !wasActive.current)
      pageContent.current
        ?.querySelector<HTMLElement>(`[data-workbench-heading="${view}"]`)
        ?.focus();
    wasActive.current = active;
  }, [active, view]);
  const previousTab = useRef(tab);
  useEffect(() => {
    if (active && previousTab.current !== tab) {
      pageContent.current
        ?.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]')
        ?.focus();
    }
    previousTab.current = tab;
  }, [active, tab]);
  const aliasReadGeneration = useRef(0);
  const revisionListGeneration = useRef(0);
  // List discovery may seed an untouched session, but never override a chosen
  // revision, route or unfinished draft when its response arrives later.
  const selectionGeneration = useRef(0);
  const linkedRevision = useRef(revisionLocation?.revisionId);
  if (active) linkedRevision.current = revisionLocation?.revisionId;

  const loadObservedAlias = useCallback(
    async (service: AgentServiceResource, name = "stable", required = false) => {
      selectionGeneration.current++;
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
      selectionGeneration.current++;
      const generation = ++aliasReadGeneration.current;
      setAliasReadTarget({ name: "stable", required: false, revisionId });
      setAliasReadLoading(true);
      setAliasReadError(undefined);
      setObservedAlias(undefined);
      setOpenedAlias(undefined);
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

  useEffect(() => {
    if (!openedService || !active || !revisionLocation?.revisionId) return;
    void openServiceRevision(openedService, revisionLocation.revisionId);
    const generation = aliasReadGeneration.current;
    return () => {
      if (aliasReadGeneration.current === generation) {
        aliasReadGeneration.current++;
        setAliasReadLoading(false);
      }
    };
  }, [active, openedService, openServiceRevision, revisionLocation?.revisionId]);

  const selectRevision = (revisionId: string, nextTab: ServiceTab) => {
    selectionGeneration.current++;
    onRevisionNavigate(revisionId, nextTab);
    if (revisionLocation?.revisionId === revisionId && openedService)
      void openServiceRevision(openedService, revisionId);
  };

  const loadRevisionPage = useCallback(
    async (service: AgentServiceResource, cursor?: string) => {
      const selection = selectionGeneration.current;
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
      setRevisionHistory((current) => mergeRevisionHistory(current, result.page.items));
      if (
        cursor === undefined &&
        selection === selectionGeneration.current &&
        selection === 0 &&
        !linkedRevision.current
      ) {
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
  );
  const commandFocusKey = `${currentStage}/${draftFormGeneration}`;
  const previousCommand = useRef(commandFocusKey);
  useEffect(() => {
    const changed = previousCommand.current !== commandFocusKey;
    previousCommand.current = commandFocusKey;
    if (!changed || !active || document.activeElement !== document.body) return;
    const heading = Array.from(
      pageContent.current?.querySelectorAll<HTMLElement>("[data-command-heading]") ?? [],
    ).find((element) => !element.closest("[hidden], [inert]"));
    heading?.focus();
    // A command transition can remove its initiating button. Restore focus only
    // when no remaining control has it, including a fresh draft in the same stage.
  }, [active, commandFocusKey]);

  const openService = (service: AgentServiceResource, reset = false) => {
    if (!reset && openedService?.metadata.id === service.metadata.id) {
      onNavigate("workflow");
      return;
    }
    aliasReadGeneration.current++;
    revisionListGeneration.current++;
    selectionGeneration.current = 0;
    setObservedAlias(undefined);
    setOpenedAlias(undefined);
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
    onNavigate("workflow");
    void loadRevisionPage(service);
    // Closing a lifecycle decision explicitly reconciles the current context.
    // Preserve an exact linked revision; otherwise discover the current route.
    if (revisionLocation?.revisionId)
      void openServiceRevision(service, revisionLocation.revisionId);
  };

  useEffect(() => {
    if (initialResource) void loadRevisionPage(initialResource);
  }, [initialResource, loadRevisionPage]);

  const startServiceRevision = () => {
    selectionGeneration.current++;
    onRevisionNavigate(undefined, "build");
    if (draftCompleted) {
      setDraftFormGeneration((current) => current + 1);
      setDraftCompleted(false);
    }
    aliasReadGeneration.current++;
    setAliasReadLoading(false);
    setAliasReadError(undefined);
    setObservedAlias(undefined);
    setOpenedAlias(undefined);
    setOpenedPublishedRevision(undefined);
    setOpenedRevision(undefined);
    setRetirementRevision(undefined);
    setWithdrawalAlias(undefined);
  };

  const openDraftRevision = (revision: ServiceRevisionResource) => {
    selectionGeneration.current++;
    onRevisionNavigate(revision.metadata.id, "build");
    aliasReadGeneration.current++;
    setAliasReadLoading(false);
    setAliasReadError(undefined);
    revisionListGeneration.current++;
    setOpenedAlias(undefined);
    setOpenedPublishedRevision(undefined);
    setOpenedRevision(revision);
    setRevisionHistory((current) => mergeRevisionHistory(current, [historyFromDraft(revision)]));
  };

  const assignPublishedRevision = (revision: PublishedServiceRevisionResource) => {
    selectRevision(revision.metadata.id, "deployments");
  };

  const composeAliasInvocation = (alias: ServiceAliasResource) => {
    selectTab("runs");
    setObservedAlias(alias);
    setOpenedAlias(alias);
  };

  const historyPanel = (
    <>
      {openedService && (
        <ServiceRevisionHistoryWorkflow
          titleId={revisionHistoryTitle}
          client={client}
          isolationDomainId={isolationDomainId}
          serviceId={openedService.metadata.id}
          error={revisionListError}
          isLoading={revisionListLoading}
          isLoadingMore={revisionListLoadingMore}
          nextCursor={revisionListNextCursor}
          onLoadMore={() =>
            revisionListNextCursor && void loadRevisionPage(openedService, revisionListNextCursor)
          }
          onRetry={() => void loadRevisionPage(openedService)}
          onCreate={retirementRevision || withdrawalAlias ? undefined : startServiceRevision}
          onOpen={
            retirementRevision || withdrawalAlias
              ? undefined
              : (revision) => {
                  selectRevision(
                    revision.metadata.id,
                    revision.state === "published" ? "deployments" : "build",
                  );
                }
          }
          onRetire={
            retirementRevision || withdrawalAlias
              ? undefined
              : (revision) => {
                  aliasReadGeneration.current++;
                  setAliasReadLoading(false);
                  setAliasReadError(undefined);
                  setRetirementRevision(revision);
                  selectTab("versions");
                }
          }
          revisions={revisionHistory}
        />
      )}
    </>
  );
  const routesPanel = (
    <>
      {openedService && !retirementRevision && !withdrawalAlias && (
        <ServiceRouteDiscovery
          refreshGeneration={routeRefreshGeneration}
          revisionNumbers={
            new Map(
              revisionHistory.map((revision) => [revision.metadata.id, revision.revisionNumber]),
            )
          }
          client={client}
          scope={{ isolationDomainId, serviceId: openedService.metadata.id }}
          canWithdraw
          onWithdraw={(alias) => {
            aliasReadGeneration.current++;
            setAliasReadLoading(false);
            setAliasReadError(undefined);
            setWithdrawalAlias(alias);
            selectTab("deployments");
          }}
          onSelect={
            revisionListLoading || revisionListError
              ? undefined
              : (alias) => {
                  selectTab("runs");
                  void loadObservedAlias(openedService, alias.name, true);
                }
          }
        />
      )}
    </>
  );
  const withdrawalPanel = (
    <>
      {withdrawalAlias &&
      openedService &&
      withdrawalAlias.metadata.isolationDomainId === isolationDomainId &&
      withdrawalAlias.serviceId === openedService.metadata.id ? (
        <ServiceAliasWithdrawWorkflow
          compact
          serviceName={openedService.name}
          alias={withdrawalAlias}
          canWithdraw
          client={client}
          onClose={() => {
            openService(openedService, true);
            selectTab("deployments");
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
    </>
  );
  const retirementPanel = (
    <>
      {retirementRevision &&
        openedService &&
        retirementRevision.metadata.isolationDomainId === isolationDomainId &&
        retirementRevision.serviceId === openedService.metadata.id && (
          <ServiceRevisionRetireWorkflow
            compact
            serviceName={openedService.name}
            key={`${retirementRevision.metadata.id}/${retirementRevision.metadata.version}`}
            client={client}
            revision={retirementRevision}
            canRetire
            onClose={() => {
              openService(openedService, true);
              selectTab("versions");
            }}
            onRetired={(revision) =>
              setRevisionHistory((current) => mergeRevisionHistory(current, [revision]))
            }
          />
        )}
    </>
  );
  const readStatus = (
    <>
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
          <p>
            {aliasReadTarget.revisionId
              ? "Reading the selected revision and its current route preconditions."
              : "Reading the exact alias state before routing or invoking this service."}
          </p>
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
            {aliasReadTarget.revisionId ? "Retry revision read" : "Retry route discovery"}
          </Button>
        </section>
      )}
    </>
  );
  const commandsAvailable = !(
    (active &&
      revisionLocation?.revisionId &&
      revisionLocation.revisionId !== aliasReadTarget.revisionId) ||
    (openedService && revisionHistory.length === 0 && (revisionListLoading || revisionListError)) ||
    retirementRevision ||
    withdrawalAlias ||
    (openedService && (aliasReadLoading || aliasReadError))
  );

  const retainedCommands = (
    kind: "publication" | "route" | "invocation",
    stage: WorkflowStageIndex,
  ) =>
    openedService && (
      <RetainedServiceCommands
        kind={kind}
        active={
          active &&
          view === "workflow" &&
          tab === (kind === "publication" ? "build" : kind === "route" ? "deployments" : "runs")
        }
        client={client}
        service={openedService}
        isolationDomainId={isolationDomainId}
        enabled={currentStage === stage && commandsAvailable}
        revision={openedRevision}
        publication={openedPublishedRevision}
        alias={openedAlias}
        observedAlias={observedAlias}
        onAssignAlias={assignPublishedRevision}
        onComposeInvocation={composeAliasInvocation}
        onAliasAssigned={(alias) => {
          if (
            alias.metadata.isolationDomainId === isolationDomainId &&
            alias.serviceId === openedService.metadata.id
          )
            setRouteRefreshGeneration((v) => v + 1);
        }}
        onPublished={(revision) => {
          if (
            revision.metadata.isolationDomainId !== isolationDomainId ||
            revision.serviceId !== openedService.metadata.id
          )
            return;
          revisionListGeneration.current++;
          setRevisionListLoading(false);
          setRevisionListLoadingMore(false);
          setRevisionHistory((current) => mergeRevisionHistory(current, [revision]));
        }}
        onInvocationCreated={(reference) => {
          if (reference.isolationDomainId === isolationDomainId)
            setRunRefreshGeneration((v) => v + 1);
        }}
        onOpenInvocation={(reference) => onRunNavigate({ invocationId: reference.invocationId })}
      />
    );

  if (openedService) {
    return (
      <div ref={pageContent}>
        <ServiceWorkbenchView
          active={active}
          runRefreshGeneration={runRefreshGeneration}
          runLocation={runLocation}
          onRunNavigate={onRunNavigate}
          client={client}
          isolationDomainId={isolationDomainId}
          view={view}
          tab={tab}
          commandTab={currentStage <= 2 ? "build" : currentStage === 3 ? "deployments" : "runs"}
          nextAction={workflowSteps[currentStage].shortLabel}
          openedService={openedService}
          openedRevision={openedRevision}
          selectedHistory={
            revisionHistory.find(
              (revision) => revision.metadata.id === openedRevision?.metadata.id,
            ) ??
            openedPublishedRevision ??
            openedRevision
          }
          openedAlias={openedAlias}
          retirementRevision={retirementRevision}
          withdrawalAlias={withdrawalAlias}
          revisionListLoading={revisionListLoading}
          revisionListError={revisionListError}
          aliasReadLoading={aliasReadLoading}
          aliasReadError={aliasReadError}
          readStatus={readStatus}
          publicationContent={retainedCommands("publication", 2)}
          routingContent={retainedCommands("route", 3)}
          invocationContent={retainedCommands("invocation", 4)}
          draftActive={currentStage === 1 && commandsAvailable}
          draftContent={
            <ServiceRevisionDraftWorkflow
              key={draftFormGeneration}
              compact
              canCreateRevision
              client={client}
              isolationDomainId={isolationDomainId}
              serviceId={openedService.metadata.id}
              onRevisionCreated={() => setDraftCompleted(true)}
              onOpenRevision={openDraftRevision}
            />
          }
          historyPanel={historyPanel}
          routesPanel={routesPanel}
          withdrawalPanel={withdrawalPanel}
          retirementPanel={retirementPanel}
          selectTab={selectTab}
          backLabel={onOpenWorkspace ? "Back to workspace" : undefined}
          onBack={onOpenWorkspace ?? (() => onNavigate("services"))}
          onCreateRevision={startServiceRevision}
        />
      </div>
    );
  }
  return (
    <div ref={pageContent}>
      <section aria-labelledby={`${headingPrefix}-workflow`}>
        <header className="workbench-page-heading workbench-page-heading--compact">
          <div>
            <p className="workbench-breadcrumb">Agents / New service</p>
            <h1 id={`${headingPrefix}-workflow`} data-workbench-heading="workflow" tabIndex={-1}>
              Create an agent service
            </h1>
            <p>Give the service a name, then configure its first revision.</p>
          </div>
          <Button onPress={() => onNavigate("services")} variant="quiet">
            Back to services
          </Button>
        </header>
        <AgentServiceCreateWorkflow
          compact
          canCreate={active}
          client={client}
          isolationDomainId={isolationDomainId}
          onOpenService={(service) => {
            openService(service);
            onServiceOpened(service);
          }}
        />
      </section>
    </div>
  );
}
