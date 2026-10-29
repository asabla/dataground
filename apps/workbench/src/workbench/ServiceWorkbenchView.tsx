import { Button, CopyButton, StatusBadge, Tabs } from "@dataground/ui";
import { type ReactNode, useEffect, useRef, useState } from "react";
import type { ServiceAliasResource } from "../aliases";
import type { DataGroundClient } from "../contracts/client";
import { InvocationHistoryWorkflow } from "../invocations/InvocationHistoryWorkflow";
import type { RunLocation } from "../invocations/navigation";
import type {
  ServiceRevisionFailure,
  ServiceRevisionHistoryResource,
  ServiceRevisionResource,
} from "../revisions";
import type { AgentServiceResource } from "../services";
import { isServiceTab, type ServiceTab, type WorkbenchView } from "./navigation";

interface ServiceWorkbenchViewProps {
  active: boolean;
  runRefreshGeneration?: number;
  runLocation?: RunLocation;
  onRunNavigate: (location: RunLocation) => void;
  client: DataGroundClient;
  isolationDomainId: string;
  view: WorkbenchView;
  tab: ServiceTab;
  commandTab: ServiceTab;
  nextAction: string;
  openedService: AgentServiceResource;
  openedRevision?: ServiceRevisionResource;
  selectedHistory?: ServiceRevisionHistoryResource;
  openedAlias?: ServiceAliasResource;
  retirementRevision?: ServiceRevisionHistoryResource;
  withdrawalAlias?: ServiceAliasResource;
  revisionListLoading: boolean;
  revisionListError?: ServiceRevisionFailure;
  aliasReadLoading: boolean;
  aliasReadError?: unknown;
  readStatus: ReactNode;
  publicationContent: ReactNode;
  routingContent: ReactNode;
  invocationContent: ReactNode;
  draftContent: ReactNode;
  draftActive: boolean;
  historyPanel: ReactNode;
  routesPanel: ReactNode;
  withdrawalPanel: ReactNode;
  retirementPanel: ReactNode;
  selectTab: (tab: ServiceTab) => void;
  backLabel?: string;
  onBack: () => void;
  onCreateRevision: () => void;
}

export function ServiceWorkbenchView({
  active,
  runRefreshGeneration = 0,
  runLocation,
  onRunNavigate,
  client,
  isolationDomainId,
  view,
  tab,
  commandTab,
  nextAction,
  openedService,
  openedRevision,
  selectedHistory,
  openedAlias,
  retirementRevision,
  withdrawalAlias,
  revisionListLoading,
  revisionListError,
  aliasReadLoading,
  aliasReadError,
  readStatus,
  publicationContent,
  routingContent,
  invocationContent,
  draftContent,
  draftActive,
  historyPanel,
  routesPanel,
  withdrawalPanel,
  retirementPanel,
  selectTab,
  backLabel = "Back to services",
  onBack,
  onCreateRevision,
}: ServiceWorkbenchViewProps) {
  const [historyInspection, setHistoryInspection] = useState(false);
  const surface = useRef<HTMLDivElement>(null);
  const decisionOpen = !!(retirementRevision || withdrawalAlias);
  const previousDecisionOpen = useRef(decisionOpen);
  useEffect(() => {
    const closed = previousDecisionOpen.current && !decisionOpen;
    previousDecisionOpen.current = decisionOpen;
    if (!closed || !active || document.activeElement !== document.body) return;
    // Closing replaces the lifecycle page. Its list may still be loading, but
    // the selected resource tab is a stable and visible return location.
    surface.current?.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]')?.focus();
  }, [active, decisionOpen]);
  return (
    <div className="service-workbench" ref={surface}>
      <header className="workbench-page-heading workbench-page-heading--compact">
        <div>
          <p className="workbench-breadcrumb">Agents / Service</p>
          <h1 data-workbench-heading={view} tabIndex={-1}>
            {openedService.name}
          </h1>
          <p>{openedService.description || "No description supplied."}</p>
        </div>
        <Button onPress={() => onBack()} variant="quiet">
          {backLabel}
        </Button>
      </header>
      {readStatus}
      {revisionListLoading && <p role="status">Loading revisions…</p>}
      {revisionListError && tab !== "versions" && (
        <div role="alert" className="workbench-inline-error">
          <p>{revisionListError.message}</p>
          <Button onPress={() => selectTab("versions")}>Review revision discovery</Button>
        </div>
      )}
      {(retirementRevision || withdrawalAlias) && (
        <p role="status">
          A lifecycle decision is open in {retirementRevision ? "Versions" : "Deployments"}.
          <Button
            variant="quiet"
            onPress={() => selectTab(retirementRevision ? "versions" : "deployments")}
          >
            Return to decision
          </Button>
        </p>
      )}
      <Tabs
        label="Service views"
        selectedId={tab}
        onSelectionChange={(id) => {
          if (isServiceTab(id)) selectTab(id);
        }}
        items={[
          {
            id: "overview",
            label: "Overview",
            content: (
              <section className="service-overview" aria-label="Service overview">
                <div className="service-overview__summary">
                  <h2>Current work</h2>
                  <dl>
                    <div>
                      <dt>Selected version</dt>
                      <dd>
                        {selectedHistory
                          ? `Revision ${selectedHistory.revisionNumber}`
                          : "No version selected"}
                      </dd>
                    </div>
                    <div>
                      <dt>Version state</dt>
                      <dd>
                        <StatusBadge
                          tone={selectedHistory?.state === "published" ? "success" : "neutral"}
                        >
                          {selectedHistory?.state ??
                            (revisionListLoading ? "Loading" : "Unconfirmed")}
                        </StatusBadge>
                      </dd>
                    </div>
                    <div>
                      <dt>Selected route</dt>
                      <dd>{openedAlias?.name ?? "No route selected"}</dd>
                    </div>
                  </dl>
                  <Button
                    onPress={() => selectTab(commandTab)}
                    isDisabled={
                      revisionListLoading ||
                      !!revisionListError ||
                      aliasReadLoading ||
                      !!aliasReadError
                    }
                  >
                    {nextAction}
                  </Button>
                </div>
                <div className="service-overview__summary">
                  <h2>Service lifecycle</h2>
                  <p>
                    Build an immutable revision, publish it, then assign a route. Publication and
                    route assignment are separate actions.
                  </p>
                  <Button variant="quiet" onPress={() => selectTab("versions")}>
                    View versions
                  </Button>
                  <Button variant="quiet" onPress={() => selectTab("deployments")}>
                    View deployments
                  </Button>
                </div>
                <details>
                  <summary>Resource details</summary>
                  <dl>
                    <div>
                      <dt>Service identifier</dt>
                      <dd>
                        <code>{openedService.metadata.id}</code>{" "}
                        <CopyButton label="service identifier" value={openedService.metadata.id} />
                      </dd>
                    </div>
                    <div>
                      <dt>Created by</dt>
                      <dd>
                        <code>{openedService.metadata.createdBy}</code>
                      </dd>
                    </div>
                  </dl>
                </details>
              </section>
            ),
          },
          {
            id: "build",
            label: "Build",
            content: (
              <>
                <div hidden={!draftActive} inert={!draftActive}>
                  {draftContent}
                </div>
                {publicationContent}
                {commandTab !== "build" && (
                  <section>
                    <h2>Revision definition</h2>
                    <p>
                      The selected revision is published. Create a new revision to change its
                      definition.
                    </p>
                    <dl>
                      <div>
                        <dt>Runtime profile</dt>
                        <dd>{openedRevision?.runtimeProfile}</dd>
                      </div>
                    </dl>
                    <Button
                      onPress={onCreateRevision}
                      isDisabled={!!retirementRevision || !!withdrawalAlias}
                    >
                      New revision
                    </Button>
                  </section>
                )}
              </>
            ),
          },
          {
            id: "versions",
            label: "Versions",
            content: (
              <>
                {historyPanel}
                {retirementPanel}
              </>
            ),
          },
          {
            id: "deployments",
            label: "Deployments",
            content: (
              <>
                {routesPanel}
                {withdrawalPanel}
                {routingContent}
              </>
            ),
          },
          {
            id: "runs",
            label: "Runs",
            content: (
              <>
                <div hidden={historyInspection || !!runLocation?.invocationId}>
                  {invocationContent}
                </div>
                <div>
                  <InvocationHistoryWorkflow
                    active={active && tab === "runs"}
                    refreshGeneration={runRefreshGeneration}
                    location={runLocation ?? {}}
                    onNavigate={onRunNavigate}
                    onInspectionChange={setHistoryInspection}
                    client={client}
                    target={{ isolationDomainId, serviceId: openedService.metadata.id }}
                  />
                </div>
                {commandTab !== "runs" && !runLocation?.invocationId && (
                  <p>
                    Select a published route in{" "}
                    <Button variant="quiet" onPress={() => selectTab("deployments")}>
                      Deployments
                    </Button>{" "}
                    to start a run.
                  </p>
                )}
              </>
            ),
          },
        ]}
      />
    </div>
  );
}
