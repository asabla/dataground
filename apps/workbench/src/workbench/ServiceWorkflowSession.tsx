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
  type AgentServiceInvocationSelection,
  type AgentServiceResource,
} from "../services";
import type { ServiceTab, WorkbenchPresentation, WorkbenchView } from "./navigation";

import { ServiceWorkbenchView } from "./ServiceWorkbenchView";

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

interface ServiceWorkflowSessionProps {
  presentation: WorkbenchPresentation;
  selectedTab?: ServiceTab;
  client: DataGroundClient;
  isolationDomainId: string;
  initialService?: AgentServiceResource;
  active: boolean;
  view: WorkbenchView;
  onNavigate: (view: WorkbenchView, tab?: ServiceTab) => void;
  onServiceOpened: (service: AgentServiceResource) => void;
}

export function ServiceWorkflowSession({
  client,
  isolationDomainId,
  presentation,
  selectedTab,
  initialService,
  active,
  view,
  onNavigate,
  onServiceOpened,
}: ServiceWorkflowSessionProps) {
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
  const [workflowVisited, setWorkflowVisited] = useState(view === "workflow");
  const [historyVisited, setHistoryVisited] = useState(view === "interactions");
  const setView = onNavigate;
  useEffect(() => {
    if (active && view === "workflow") setWorkflowVisited(true);
    if (active && view === "interactions") setHistoryVisited(true);
  }, [active, view]);
  const wasActive = useRef(false);
  useEffect(() => {
    if (active && (presentation === "classic" || !wasActive.current))
      pageContent.current
        ?.querySelector<HTMLElement>(`[data-workbench-heading="${view}"]`)
        ?.focus();
    wasActive.current = active;
  }, [active, view, presentation]);
  const previousTab = useRef(tab);
  useEffect(() => {
    if (active && presentation === "workbench" && previousTab.current !== tab) {
      pageContent.current
        ?.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]')
        ?.focus();
    }
    previousTab.current = tab;
  }, [active, presentation, tab]);
  const aliasReadGeneration = useRef(0);
  const revisionListGeneration = useRef(0);

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
  const openService = (service: AgentServiceResource, reset = false) => {
    if (!reset && openedService?.metadata.id === service.metadata.id) {
      onNavigate("workflow");
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
    onNavigate("workflow");
    void loadRevisionPage(service);
  };

  useEffect(() => {
    if (initialResource) void loadRevisionPage(initialResource);
  }, [initialResource, loadRevisionPage]);

  const startServiceRevision = () => {
    selectTab("build");
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
      presentation={presentation}
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
        selectTab("deployments");
        if (openedService) void openServiceRevision(openedService, revision.metadata.id);
      }}
      onComposeInvocation={(alias) => {
        selectTab("runs");
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
        selectTab("build");
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
        openService(service);
        onServiceOpened(service);
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
                  selectTab(revision.state === "published" ? "deployments" : "build");
                  void openServiceRevision(openedService, revision.metadata.id);
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
                  setOpenedQuestion(undefined);
                  setOpenedApproval(undefined);
                  setOpenedArtifact(undefined);
                  setOpenedInvocation(undefined);
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
          alias={withdrawalAlias}
          canWithdraw
          client={client}
          onClose={() => {
            openService(openedService, true);
            selectTab("deployments");
            document.getElementById(revisionHistoryTitle)?.focus();
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
            key={`${retirementRevision.metadata.id}/${retirementRevision.metadata.version}`}
            client={client}
            revision={retirementRevision}
            canRetire
            onClose={() => {
              openService(openedService, true);
              selectTab("versions");
              document.getElementById(revisionHistoryTitle)?.focus();
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
    </>
  );
  const commandContent =
    openedService && revisionHistory.length === 0 && (revisionListLoading || revisionListError)
      ? null
      : retirementRevision ||
          withdrawalAlias ||
          (openedService && (aliasReadLoading || aliasReadError))
        ? null
        : workflow;

  if (presentation === "workbench" && openedService) {
    return (
      <div ref={pageContent}>
        <ServiceWorkbenchView
          client={client}
          isolationDomainId={isolationDomainId}
          view={view}
          tab={tab}
          commandTab={currentStage <= 2 ? "build" : currentStage === 3 ? "deployments" : "runs"}
          nextAction={workflowSteps[currentStage].shortLabel}
          openedService={openedService}
          openedRevision={openedRevision}
          selectedHistory={revisionHistory.find(
            (revision) => revision.metadata.id === openedRevision?.metadata.id,
          )}
          openedAlias={openedAlias}
          openedInvocation={openedInvocation}
          retirementRevision={retirementRevision}
          withdrawalAlias={withdrawalAlias}
          revisionListLoading={revisionListLoading}
          revisionListError={revisionListError}
          aliasReadLoading={aliasReadLoading}
          aliasReadError={aliasReadError}
          readStatus={readStatus}
          commandContent={commandContent}
          historyPanel={historyPanel}
          routesPanel={routesPanel}
          withdrawalPanel={withdrawalPanel}
          retirementPanel={retirementPanel}
          selectTab={selectTab}
          onBack={() => setView("services")}
          onCreateRevision={startServiceRevision}
          onCloseInvocation={() => {
            setOpenedInvocation(undefined);
            setOpenedQuestion(undefined);
            setOpenedApproval(undefined);
            setOpenedArtifact(undefined);
          }}
        />
      </div>
    );
  }
  return (
    <div ref={pageContent}>
      {(workflowVisited || view === "workflow") && (
        <section aria-labelledby={`${headingPrefix}-workflow`} hidden={view !== "workflow"}>
          <div className="workbench-page-heading workbench-page-heading--compact">
            <div>
              <p className="workbench-breadcrumb">
                Agent services / {openedService?.name ?? "New service"}
              </p>
              <h1 id={`${headingPrefix}-workflow`} data-workbench-heading="workflow" tabIndex={-1}>
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
              {historyPanel}
              {routesPanel}
              {withdrawalPanel}
              {retirementPanel}
              {readStatus}
            </div>
          </div>
        </section>
      )}

      {(historyVisited || view === "interactions") && (
        <section aria-labelledby={`${headingPrefix}-interactions`} hidden={view !== "interactions"}>
          <div className="workbench-page-heading workbench-page-heading--compact">
            <div>
              <p className="workbench-breadcrumb">
                {openedService ? `${openedService.name} / Interactions` : "AI workspace"}
              </p>
              <h1
                id={`${headingPrefix}-interactions`}
                data-workbench-heading="interactions"
                tabIndex={-1}
              >
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
            <section className="workbench-empty" aria-labelledby={`${headingPrefix}-empty`}>
              <span aria-hidden="true" className="workbench-empty__mark">
                ↗
              </span>
              <h2 id={`${headingPrefix}-empty`}>No interaction is open</h2>
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
          hidden={view !== "workflow" && !(view === "interactions" && openedInvocation)}
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
  );
}
