import { Button, StatusBadge, TextField } from "@dataground/ui";
import { useState } from "react";
import type { ServiceAliasResource } from "./aliases";
import type { InvocationArtifactReference } from "./artifacts";
import { createDataGroundClient, type DataGroundClient } from "./contracts/client";
import type { PublishedServiceRevisionResource, ServiceRevisionResource } from "./revisions";
import {
  AgentServiceAuthoringWorkflow,
  type AgentServiceInvocationSelection,
  type AgentServiceResource,
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
  const [openedAlias, setOpenedAlias] = useState<ServiceAliasResource>();
  const [openedArtifact, setOpenedArtifact] = useState<InvocationArtifactReference>();
  const [openedInvocation, setOpenedInvocation] = useState<AgentServiceInvocationSelection>();
  const [openedPublishedRevision, setOpenedPublishedRevision] =
    useState<PublishedServiceRevisionResource>();
  const [openedRevision, setOpenedRevision] = useState<ServiceRevisionResource>();
  const [openedService, setOpenedService] = useState<AgentServiceResource>();
  const [sessionServices, setSessionServices] = useState<AgentServiceResource[]>([]);
  const [view, setView] = useState<WorkbenchView>("services");

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
    setOpenedAlias(undefined);
    setOpenedArtifact(undefined);
    setOpenedInvocation(undefined);
    setOpenedPublishedRevision(undefined);
    setOpenedRevision(undefined);
    setOpenedService(undefined);
  };

  const selectSessionService = (service: AgentServiceResource) => {
    setOpenedAlias(undefined);
    setOpenedArtifact(undefined);
    setOpenedInvocation(undefined);
    setOpenedPublishedRevision(undefined);
    setOpenedRevision(undefined);
    setOpenedService(service);
    setView("workflow");
  };

  const workflow = (
    <AgentServiceAuthoringWorkflow
      canAssignAlias
      canCancelInvocation
      canCreateRevision
      canCreateService
      canPublishRevision
      canInvokeService
      client={client}
      focusCurrentStage
      isolationDomainId={isolationDomainId}
      onAssignAlias={(revision) => {
        setOpenedAlias(undefined);
        setOpenedArtifact(undefined);
        setOpenedInvocation(undefined);
        setOpenedPublishedRevision(revision);
      }}
      onComposeInvocation={(alias) => {
        setOpenedAlias(alias);
        setOpenedArtifact(undefined);
        setOpenedInvocation(undefined);
      }}
      onCloseArtifact={() => setOpenedArtifact(undefined)}
      onInspectArtifact={setOpenedArtifact}
      onOpenInvocation={(selection) => {
        setOpenedArtifact(undefined);
        setOpenedInvocation(selection);
        setView("interactions");
      }}
      onOpenRevision={(revision) => {
        setOpenedAlias(undefined);
        setOpenedArtifact(undefined);
        setOpenedInvocation(undefined);
        setOpenedPublishedRevision(undefined);
        setOpenedRevision(revision);
      }}
      onOpenService={(service) => {
        setOpenedAlias(undefined);
        setOpenedArtifact(undefined);
        setOpenedInvocation(undefined);
        setOpenedPublishedRevision(undefined);
        setOpenedRevision(undefined);
        setOpenedService(service);
        setSessionServices((current) => {
          const existing = current.findIndex(
            (candidate) => candidate.metadata.id === service.metadata.id,
          );
          if (existing === -1) return [...current, service];
          return current.map((candidate, index) => (index === existing ? service : candidate));
        });
      }}
      selectedAlias={openedAlias}
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
                  <dt>Opened in this session</dt>
                  <dd>
                    {sessionServices.length} {sessionServices.length === 1 ? "service" : "services"}
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
                      : sessionServices.length > 0
                        ? "Open a service"
                        : "Create a service"}
                  </dd>
                </div>
              </dl>

              {sessionServices.length > 0 ? (
                <section aria-label="Agent services" className="workbench-resource-list">
                  <div className="workbench-resource-list__head" aria-hidden="true">
                    <span>Service</span>
                    <span>Status</span>
                    <span>Next action</span>
                    <span />
                  </div>
                  {sessionServices.map((service) => {
                    const isSelected = service.metadata.id === openedService?.metadata.id;
                    const rowPresentation = isSelected
                      ? presentation
                      : { label: "Opened", tone: "neutral" as const };
                    return (
                      <button
                        className="workbench-resource-row"
                        key={service.metadata.id}
                        onClick={() => {
                          if (isSelected && openedInvocation) setView("interactions");
                          else if (isSelected) setView("workflow");
                          else selectSessionService(service);
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
                </section>
              ) : (
                <section className="workbench-empty" aria-labelledby="empty-services-title">
                  <span aria-hidden="true" className="workbench-empty__mark">
                    ◇
                  </span>
                  <h2 id="empty-services-title">No services opened in this session</h2>
                  <p>
                    Create a service to begin the governed vertical slice. A service-list contract
                    is not available yet, so the Workbench does not invent or discover resources
                    outside this session.
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
                <div className="workbench-stage">{workflow}</div>
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
                  <Button onPress={() => setView("services")} variant="quiet">
                    Close
                  </Button>
                )}
              </div>
              {openedInvocation ? (
                <div className="workbench-interaction">{workflow}</div>
              ) : (
                <section className="workbench-empty" aria-labelledby="empty-interactions-title">
                  <span aria-hidden="true" className="workbench-empty__mark">
                    ↗
                  </span>
                  <h2 id="empty-interactions-title">No interaction is open</h2>
                  <p>
                    Publish and route a service, then start an interaction from its guided flow.
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
