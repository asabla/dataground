import { Button, StatusBadge, TextField } from "@dataground/ui";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  isServiceAliasRoutedToRevision,
  readServiceAlias,
  type ServiceAliasFailure,
  type ServiceAliasResource,
} from "./aliases";
import { ServiceAliasWithdrawWorkflow } from "./aliases/ServiceAliasWithdrawWorkflow";
import { ServiceRouteDiscovery } from "./aliases/ServiceRouteDiscovery";
import type { InvocationApprovalReference } from "./approvals";
import type { InvocationArtifactReference } from "./artifacts";
import { createDataGroundClient, type DataGroundClient } from "./contracts/client";
import { InvocationHistoryWorkflow } from "./invocations/InvocationHistoryWorkflow";
import type { InvocationQuestionReference } from "./questions";
import {
  listServiceRevisions,
  type PublishedServiceRevisionResource,
  readServiceRevision,
  resumeServiceRevision,
  type ServiceRevisionFailure,
  ServiceRevisionHistoryPanel,
  type ServiceRevisionHistoryResource,
  type ServiceRevisionResource,
} from "./revisions";
import { ServiceRevisionRetireWorkflow } from "./revisions/ServiceRevisionRetireWorkflow";
import {
  AgentServiceAuthoringWorkflow,
  type AgentServiceFailure,
  type AgentServiceInvocationSelection,
  type AgentServiceResource,
  listAgentServices,
} from "./services";

const DEFAULT_ISOLATION_DOMAIN_ID = "iso_00000000000000000001";
const ISOLATION_DOMAIN_ID_PATTERN = /^iso_[0-9a-z]{20,32}$/u;
const BEARER_TOKEN_PATTERN = /^[A-Za-z0-9\-._~+/]+=*$/u;

interface DevelopmentScopeErrors {
  bearerToken?: string;
  isolationDomainId?: string;
}

interface DevelopmentSession {
  client: DataGroundClient;
  isolationDomainId: string;
}

type WorkbenchView = "interactions" | "services" | "workflow";
type WorkflowStageIndex = 0 | 1 | 2 | 3 | 4 | 5;

const workflowSteps = [
  { label: "Purpose", shortLabel: "Create service" },
  { label: "Runtime & access", shortLabel: "Create revision" },
  { label: "Review", shortLabel: "Publish revision" },
  { label: "Route", shortLabel: "Assign alias" },
  { label: "Run", shortLabel: "Start interaction" },
  { label: "Observe", shortLabel: "Monitor interaction" },
] as const;

export function validateDevelopmentScope(
  isolationDomainId: string,
  bearerToken: string,
): DevelopmentScopeErrors {
  const errors: DevelopmentScopeErrors = {};
  const normalizedIsolationDomainId = isolationDomainId.trim();
  if (!ISOLATION_DOMAIN_ID_PATTERN.test(normalizedIsolationDomainId)) {
    errors.isolationDomainId =
      "Use an isolation domain identifier beginning with iso_ and 20 to 32 lowercase letters or digits.";
  }

  const tokenLength = new TextEncoder().encode(bearerToken).byteLength;
  if (tokenLength < 32) {
    errors.bearerToken = "The development bearer token must be at least 32 bytes.";
  } else if (tokenLength > 8192) {
    errors.bearerToken = "The development bearer token must not exceed 8,192 bytes.";
  } else if (!BEARER_TOKEN_PATTERN.test(bearerToken)) {
    errors.bearerToken = "The token contains characters that cannot be sent in a Bearer header.";
  }
  return errors;
}

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

function servicePresentation(
  revision?: ServiceRevisionResource,
  publication?: PublishedServiceRevisionResource,
  alias?: ServiceAliasResource,
  invocation?: AgentServiceInvocationSelection,
) {
  if (invocation) return { label: "Interaction active", tone: "active" as const };
  if (alias) return { label: "Ready", tone: "success" as const };
  if (publication) return { label: "Needs route", tone: "warning" as const };
  if (revision) return { label: "Needs review", tone: "warning" as const };
  return { label: "Draft", tone: "neutral" as const };
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

interface DevelopmentWorkbenchProps {
  client: DataGroundClient;
  isolationDomainId: string;
  onDisconnect: () => void;
}

export function DevelopmentWorkbench({
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
  const [view, setView] = useState<WorkbenchView>("services");
  const aliasReadGeneration = useRef(0);
  const revisionListGeneration = useRef(0);
  const serviceListGeneration = useRef(0);

  const loadServicePage = useCallback(
    async (cursor?: string) => {
      const generation = ++serviceListGeneration.current;
      if (cursor === undefined) setServiceListLoading(true);
      else setServiceListLoadingMore(true);
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
  const presentation = servicePresentation(
    openedRevision,
    openedPublishedRevision,
    openedAlias,
    openedInvocation,
  );

  const clearWorkflow = () => {
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
  };

  const openService = (service: AgentServiceResource) => {
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
    setView("workflow");
    void loadRevisionPage(service);
  };

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
    <main className="workbench-shell">
      <header className="workbench-topbar">
        <div className="workbench-brand">
          <span aria-hidden="true" className="workbench-brand__mark">
            D
          </span>
          <strong>DataGround</strong>
        </div>
        <section aria-label="Active isolation scope" className="workbench-scope">
          <span>Development scope</span>
          <code>{isolationDomainId}</code>
        </section>
        <div className="workbench-topbar__actions">
          <StatusBadge tone="active">Local development</StatusBadge>
          <Button onPress={onDisconnect} variant="quiet">
            Disconnect
          </Button>
        </div>
      </header>

      <div className="workbench-layout">
        <aside className="workbench-sidebar">
          <p className="workbench-sidebar__label">AI workspace</p>
          <nav aria-label="Primary navigation">
            <Button
              aria-current={view === "services" || view === "workflow" ? "page" : undefined}
              className="workbench-nav-button"
              onPress={() => setView("services")}
              variant="quiet"
            >
              <span aria-hidden="true">◇</span>
              Agent services
            </Button>
            <Button
              aria-current={view === "interactions" ? "page" : undefined}
              className="workbench-nav-button"
              onPress={() => setView("interactions")}
              variant="quiet"
            >
              <span aria-hidden="true">↗</span>
              Interactions
            </Button>
          </nav>
        </aside>

        <div className="workbench-main">
          {view === "services" && (
            <section aria-labelledby="services-title">
              <div className="workbench-page-heading">
                <div>
                  <p className="workbench-kicker">AI workspace</p>
                  <h1 id="services-title">Agent services</h1>
                  <p>
                    Build, publish, and operate governed agents through stable product identities.
                  </p>
                </div>
                <Button
                  onPress={() => {
                    clearWorkflow();
                    setView("workflow");
                  }}
                  variant="primary"
                >
                  New service
                </Button>
              </div>

              <dl className="workbench-summary" aria-label="Service summary">
                <div>
                  <dt>Available in scope</dt>
                  <dd>
                    {scopedServices.length} {scopedServices.length === 1 ? "service" : "services"}
                  </dd>
                </div>
                <div>
                  <dt>Active interactions</dt>
                  <dd>{openedInvocation ? "1 interaction" : "None"}</dd>
                </div>
                <div>
                  <dt>Next action</dt>
                  <dd>
                    {openedService
                      ? workflowSteps[currentStage].shortLabel
                      : scopedServices.length > 0
                        ? "Open a service"
                        : "Create a service"}
                  </dd>
                </div>
              </dl>

              {serviceListError && scopedServices.length > 0 && (
                <section className="workbench-inline-error" role="alert">
                  <p>{serviceListError.message}</p>
                  <Button onPress={() => void loadServicePage()} variant="quiet">
                    Retry service discovery
                  </Button>
                </section>
              )}

              {scopedServices.length > 0 ? (
                <section aria-label="Agent services" className="workbench-resource-list">
                  <div className="workbench-resource-list__head" aria-hidden="true">
                    <span>Service</span>
                    <span>Status</span>
                    <span>Next action</span>
                    <span />
                  </div>
                  {scopedServices.map((service) => {
                    const isSelected = service.metadata.id === openedService?.metadata.id;
                    const rowPresentation = isSelected
                      ? presentation
                      : { label: "Available", tone: "neutral" as const };
                    return (
                      <button
                        className="workbench-resource-row"
                        key={service.metadata.id}
                        onClick={() => {
                          if (isSelected && openedInvocation) setView("interactions");
                          else if (isSelected) setView("workflow");
                          else openService(service);
                        }}
                        type="button"
                      >
                        <span className="workbench-resource-row__name">
                          <strong>{service.name}</strong>
                          <span>{service.description ?? "No description"}</span>
                        </span>
                        <StatusBadge tone={rowPresentation.tone}>
                          {rowPresentation.label}
                        </StatusBadge>
                        <span className="workbench-resource-row__secondary">
                          {isSelected ? workflowSteps[currentStage].shortLabel : "Continue setup"}
                        </span>
                        <span aria-hidden="true">›</span>
                      </button>
                    );
                  })}
                  {serviceListNextCursor && (
                    <div className="workbench-resource-list__more">
                      <Button
                        isDisabled={serviceListLoadingMore}
                        onPress={() => void loadServicePage(serviceListNextCursor)}
                        variant="quiet"
                      >
                        {serviceListLoadingMore ? "Loading more services…" : "Load more services"}
                      </Button>
                    </div>
                  )}
                </section>
              ) : serviceListLoading ? (
                <section aria-busy="true" className="workbench-empty">
                  <span aria-hidden="true" className="workbench-empty__mark">
                    ◇
                  </span>
                  <h2>Loading agent services</h2>
                  <p>Reading the authoritative resource list for this isolation scope.</p>
                </section>
              ) : serviceListError ? (
                <section className="workbench-empty" aria-labelledby="service-list-error-title">
                  <span aria-hidden="true" className="workbench-empty__mark">
                    !
                  </span>
                  <h2 id="service-list-error-title">Agent services are unavailable</h2>
                  <p>{serviceListError.message}</p>
                  <Button onPress={() => void loadServicePage()}>Retry service discovery</Button>
                </section>
              ) : (
                <section className="workbench-empty" aria-labelledby="empty-services-title">
                  <span aria-hidden="true" className="workbench-empty__mark">
                    ◇
                  </span>
                  <h2 id="empty-services-title">No agent services in this scope</h2>
                  <p>
                    Create a service to begin the governed vertical slice. New resources appear here
                    from the authoritative isolation-scoped service list.
                  </p>
                  <Button
                    onPress={() => {
                      clearWorkflow();
                      setView("workflow");
                    }}
                  >
                    Create agent service
                  </Button>
                </section>
              )}
            </section>
          )}

          {view === "workflow" && (
            <section aria-labelledby="workflow-title">
              <div className="workbench-page-heading workbench-page-heading--compact">
                <div>
                  <p className="workbench-breadcrumb">
                    Agent services / {openedService?.name ?? "New service"}
                  </p>
                  <h1 id="workflow-title">{openedService?.name ?? "Create an agent service"}</h1>
                  <p>{workflowSteps[currentStage].shortLabel} to continue the governed setup.</p>
                </div>
                <Button onPress={() => setView("services")} variant="quiet">
                  Back to services
                </Button>
              </div>

              <div className="workbench-flow-layout">
                <nav aria-label="Service setup progress" className="workbench-stepper">
                  <ol>
                    {workflowSteps.map((step, index) => (
                      <li
                        aria-current={index === currentStage ? "step" : undefined}
                        className={index < currentStage ? "is-complete" : undefined}
                        key={step.label}
                      >
                        <span aria-hidden="true" className="workbench-stepper__number">
                          {index < currentStage ? "✓" : index + 1}
                        </span>
                        <span>{step.label}</span>
                      </li>
                    ))}
                  </ol>
                </nav>
                <div className="workbench-stage">
                  {openedService && (
                    <ServiceRevisionHistoryPanel
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
                        openService(openedService);
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
                          openService(openedService);
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
              </div>
            </section>
          )}

          {view === "interactions" && (
            <section aria-labelledby="interactions-title">
              <div className="workbench-page-heading workbench-page-heading--compact">
                <div>
                  <p className="workbench-breadcrumb">
                    {openedService ? `${openedService.name} / Interactions` : "AI workspace"}
                  </p>
                  <h1 id="interactions-title">
                    {openedInvocation ? "Active interaction" : "Interactions"}
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
              {openedInvocation ? (
                <div className="workbench-interaction">{workflow}</div>
              ) : openedService ? (
                <InvocationHistoryWorkflow
                  client={client}
                  target={{ isolationDomainId, serviceId: openedService.metadata.id }}
                />
              ) : (
                <section className="workbench-empty" aria-labelledby="empty-interactions-title">
                  <span aria-hidden="true" className="workbench-empty__mark">
                    ↗
                  </span>
                  <h2 id="empty-interactions-title">No interaction is open</h2>
                  <p>
                    Select a service to browse its invocation history or start a new interaction.
                  </p>
                  <Button onPress={() => setView(openedService ? "workflow" : "services")}>
                    {openedService ? "Continue service setup" : "View agent services"}
                  </Button>
                </section>
              )}
            </section>
          )}
        </div>
      </div>
    </main>
  );
}

export function App() {
  const [bearerToken, setBearerToken] = useState("");
  const [isolationDomainId, setIsolationDomainId] = useState(DEFAULT_ISOLATION_DOMAIN_ID);
  const [session, setSession] = useState<DevelopmentSession>();
  const [validationErrors, setValidationErrors] = useState<DevelopmentScopeErrors>({});

  if (session) {
    return (
      <DevelopmentWorkbench
        client={session.client}
        isolationDomainId={session.isolationDomainId}
        onDisconnect={() => {
          setSession(undefined);
          setBearerToken("");
        }}
      />
    );
  }

  return (
    <main className="connection-shell">
      <header className="connection-brand">
        <span aria-hidden="true" className="workbench-brand__mark">
          D
        </span>
        <p className="workbench-kicker">Self-hosted data and agent platform</p>
        <h1>DataGround</h1>
        <p>Open a local development scope to build and operate governed agent services.</p>
      </header>

      <section className="development-connection" aria-labelledby="development-connection-title">
        <div>
          <StatusBadge tone="warning">Development only</StatusBadge>
          <h2 id="development-connection-title">Connect to the reference API</h2>
          <p>
            The Workbench keeps this credential in memory and sends every command through the
            same-origin development proxy. The API independently authenticates and authorizes each
            request.
          </p>
        </div>
        <form
          className="development-connection__form"
          onSubmit={(event) => {
            event.preventDefault();
            const errors = validateDevelopmentScope(isolationDomainId, bearerToken);
            setValidationErrors(errors);
            if (Object.keys(errors).length > 0) return;
            setSession({
              client: createDataGroundClient("", { bearerToken }),
              isolationDomainId: isolationDomainId.trim(),
            });
            setBearerToken("");
          }}
        >
          <TextField
            description="Must match the development API configuration exactly."
            errorMessage={validationErrors.isolationDomainId}
            isRequired
            label="Isolation domain"
            maxLength={36}
            name="isolation-domain-id"
            onChange={(value) => {
              setIsolationDomainId(value);
              setValidationErrors((current) => ({
                ...current,
                isolationDomainId: undefined,
              }));
            }}
            spellCheck="false"
            value={isolationDomainId}
          />
          <TextField
            autoComplete="off"
            description="Never persisted by the Workbench or included in diagnostics."
            errorMessage={validationErrors.bearerToken}
            isRequired
            label="Development bearer token"
            maxLength={8192}
            name="development-bearer-token"
            onChange={(value) => {
              setBearerToken(value);
              setValidationErrors((current) => ({ ...current, bearerToken: undefined }));
            }}
            spellCheck="false"
            type="password"
            value={bearerToken}
          />
          <Button type="submit" variant="primary">
            Open development scope
          </Button>
        </form>
      </section>
    </main>
  );
}
