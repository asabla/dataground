import { presentTimelineEvent, type TimelineEvent } from "@dataground/patterns";
import { Button, SplitView, StatusBadge, Tabs } from "@dataground/ui";
import { useEffect, useId, useRef, useState } from "react";
import {
  ApprovalDiscoveryWorkflow,
  ApprovalWorkflow,
  type InvocationApprovalReference,
} from "../approvals";
import { ArtifactWorkflow, type InvocationArtifactReference } from "../artifacts";
import { ResourceAuditWorkflow } from "../audit/ResourceAuditWorkflow";
import type { DataGroundClient } from "../contracts/client";
import { EventTimelineWorkflow } from "../events";
import {
  type InvocationQuestionReference,
  QuestionDiscoveryWorkflow,
  QuestionWorkflow,
} from "../questions";
import type { InvocationReference } from "./client";
import { InvocationWorkflow } from "./InvocationWorkflow";
import { isRunTab, type RunTab } from "./navigation";

const isolationDomainIdPattern = /^iso_[0-9a-z]{20,32}$/u;
const invocationIdPattern = /^inv_[0-9a-z]{20,32}$/u;
const approvalIdPattern = /^apr_[0-9a-z]{20,32}$/u;
const artifactIdPattern = /^art_[0-9a-z]{20,32}$/u;

export interface InvocationInspectionWorkflowProps {
  presentation?: "classic" | "workbench";
  selectedTab?: RunTab;
  onSelectTab?: (tab: RunTab) => void;
  canCancelInvocation: boolean;
  canAnswerQuestion?: boolean;
  canResolveApproval: boolean;
  cancellationDisabledReason?: string;
  client: DataGroundClient;
  onCloseQuestion?: () => void;
  onCloseApproval: () => void;
  onCloseArtifact: () => void;
  onInspectQuestion?: (reference: InvocationQuestionReference) => void;
  onInspectApproval: (reference: InvocationApprovalReference) => void;
  onInspectArtifact: (reference: InvocationArtifactReference) => void;
  reference: InvocationReference;
  selectedQuestion?: InvocationQuestionReference;
  selectedApproval?: InvocationApprovalReference;
  selectedArtifact?: InvocationArtifactReference;
}

export function isQuestionSelectedForInvocation(
  question: InvocationQuestionReference,
  invocation: InvocationReference,
): boolean {
  return (
    isolationDomainIdPattern.test(invocation.isolationDomainId) &&
    invocationIdPattern.test(invocation.invocationId) &&
    /^qst_[0-9a-z]{20,32}$/u.test(question.questionId) &&
    question.isolationDomainId === invocation.isolationDomainId &&
    question.invocationId === invocation.invocationId
  );
}

export function isArtifactSelectedForInvocation(
  artifact: InvocationArtifactReference,
  invocation: InvocationReference,
): boolean {
  return (
    isolationDomainIdPattern.test(invocation.isolationDomainId) &&
    invocationIdPattern.test(invocation.invocationId) &&
    artifactIdPattern.test(artifact.artifactId) &&
    artifact.isolationDomainId === invocation.isolationDomainId &&
    artifact.invocationId === invocation.invocationId
  );
}

export function isApprovalSelectedForInvocation(
  approval: InvocationApprovalReference,
  invocation: InvocationReference,
): boolean {
  return (
    isolationDomainIdPattern.test(invocation.isolationDomainId) &&
    invocationIdPattern.test(invocation.invocationId) &&
    approvalIdPattern.test(approval.approvalId) &&
    approval.isolationDomainId === invocation.isolationDomainId &&
    approval.invocationId === invocation.invocationId
  );
}

function focusSection(section: HTMLElement | null) {
  section?.focus();
  section?.scrollIntoView({ block: "start" });
}

export function InvocationInspectionWorkflow({
  presentation = "classic",
  selectedTab,
  onSelectTab,
  canCancelInvocation,
  canAnswerQuestion = false,
  canResolveApproval,
  cancellationDisabledReason,
  client,
  onCloseQuestion,
  onCloseApproval,
  onCloseArtifact,
  onInspectQuestion,
  onInspectApproval,
  onInspectArtifact,
  reference,
  selectedQuestion,
  selectedApproval,
  selectedArtifact,
}: InvocationInspectionWorkflowProps) {
  const [activitySelection, setActivitySelection] = useState<{
    client: DataGroundClient;
    scope: string;
    event: TimelineEvent;
    artifactKey?: string;
  }>();
  const [retainedTab, setActiveTab] = useState<RunTab>("activity");
  const activeTab = selectedTab ?? retainedTab;
  const approvalBlockedTitleId = useId();
  const approvalInspectionTitleId = useId();
  const artifactBlockedTitleId = useId();
  const artifactInspectionTitleId = useId();
  const navigation = useRef<HTMLElement>(null);
  const overview = useRef<HTMLElement>(null);
  const activity = useRef<HTMLElement>(null);
  const requests = useRef<HTMLElement>(null);
  const audit = useRef<HTMLElement>(null);
  const inspection = useRef<HTMLElement>(null);
  const origin = useRef<
    { element: HTMLElement; client: DataGroundClient; scope: string } | undefined
  >(undefined);
  const scope = `${reference.isolationDomainId}/${reference.invocationId}`;
  const artifactKey = selectedArtifact
    ? `${selectedArtifact.isolationDomainId}/${selectedArtifact.invocationId}/${selectedArtifact.artifactId}`
    : undefined;
  const selectedEvent =
    activitySelection?.client === client &&
    activitySelection.scope === scope &&
    activitySelection.artifactKey === artifactKey
      ? activitySelection.event
      : undefined;
  const hasSelection = !!(
    selectedEvent ||
    selectedQuestion ||
    selectedApproval ||
    selectedArtifact
  );
  const selectionKey = [
    selectedEvent?.id,
    selectedQuestion?.isolationDomainId,
    selectedQuestion?.invocationId,
    selectedQuestion?.questionId,
    selectedApproval?.isolationDomainId,
    selectedApproval?.invocationId,
    selectedApproval?.approvalId,
    selectedArtifact?.isolationDomainId,
    selectedArtifact?.invocationId,
    selectedArtifact?.artifactId,
  ].join("/");

  useEffect(() => {
    if (origin.current?.client !== client || origin.current?.scope !== scope)
      origin.current = undefined;
    setActivitySelection((current) =>
      current?.client === client && current.scope === scope && current.artifactKey === artifactKey
        ? current
        : undefined,
    );
  }, [client, scope, artifactKey]);
  useEffect(() => {
    if (hasSelection && selectionKey) focusSection(inspection.current);
  }, [hasSelection, selectionKey]);

  const beginInspection = () => {
    if (document.activeElement instanceof HTMLElement) {
      origin.current = { element: document.activeElement, client, scope };
    }
    // Reopening the current item does not change its resource key.
    focusSection(inspection.current);
  };
  const inspectQuestion = onInspectQuestion
    ? (value: InvocationQuestionReference) => {
        beginInspection();
        setActivitySelection(undefined);
        onInspectQuestion(value);
      }
    : undefined;
  const inspectApproval = (value: InvocationApprovalReference) => {
    beginInspection();
    setActivitySelection(undefined);
    onInspectApproval(value);
  };
  const inspectArtifact = (value: InvocationArtifactReference) => {
    beginInspection();
    setActivitySelection(undefined);
    onInspectArtifact(value);
  };
  const closeInspection = (close: (() => void) | undefined) => {
    if (!close) return;
    const previous = origin.current;
    origin.current = undefined;
    close();
    const target =
      previous?.client === client && previous.scope === scope && previous.element.isConnected
        ? previous.element
        : navigation.current;
    focusSection(target);
    if (document.activeElement !== target) focusSection(navigation.current);
  };

  const selection = hasSelection && (
    <section
      aria-label="Selected interaction item"
      className="interaction-inspector__selection"
      ref={inspection}
      tabIndex={-1}
    >
      {selectedEvent && (
        <section aria-label="Activity inspection">
          <div className="product-workflow__inspection-heading">
            <h2>{presentTimelineEvent(selectedEvent).label}</h2>
            <Button
              variant="quiet"
              onPress={() => closeInspection(() => setActivitySelection(undefined))}
            >
              Close activity
            </Button>
          </div>
          <p>{presentTimelineEvent(selectedEvent).detail}</p>
          <dl>
            <div>
              <dt>Order</dt>
              <dd>{selectedEvent.sequence}</dd>
            </div>
            <div>
              <dt>Occurred</dt>
              <dd>
                <time dateTime={selectedEvent.occurredAt}>
                  {new Date(selectedEvent.occurredAt).toLocaleString()}
                </time>
              </dd>
            </div>
            <div>
              <dt>Evidence source</dt>
              <dd>{selectedEvent.source ?? "Not specified"}</dd>
            </div>
          </dl>
          <details>
            <summary>Event details</summary>
            <dl>
              <div>
                <dt>Event type</dt>
                <dd>{selectedEvent.type}</dd>
              </div>
              <div>
                <dt>Event identifier</dt>
                <dd>
                  <code>{selectedEvent.id}</code>
                </dd>
              </div>
              <div>
                <dt>Recorded</dt>
                <dd>{selectedEvent.recordedAt}</dd>
              </div>
              <div>
                <dt>Correlation</dt>
                <dd>
                  <code>{selectedEvent.correlationId}</code>
                </dd>
              </div>
            </dl>
          </details>
        </section>
      )}
      <div hidden={!!selectedEvent}>
        {selectedQuestion &&
          (isQuestionSelectedForInvocation(selectedQuestion, reference) ? (
            <section className="product-workflow__inspection" aria-label="Question inspection">
              <Button
                isDisabled={!onCloseQuestion}
                onPress={() => closeInspection(onCloseQuestion)}
                variant="quiet"
              >
                Close questions
              </Button>
              <QuestionWorkflow
                client={client}
                reference={selectedQuestion}
                canAnswer={canAnswerQuestion}
              />
            </section>
          ) : (
            <section className="product-workflow__blocked" role="alert">
              <StatusBadge tone="critical">Scope mismatch</StatusBadge>
              <p>
                The selected question does not belong to the active invocation. Reopen it from the
                question list or confirmed event timeline.
              </p>
            </section>
          ))}
        {selectedApproval &&
          (isApprovalSelectedForInvocation(selectedApproval, reference) ? (
            <section
              aria-labelledby={approvalInspectionTitleId}
              className="product-workflow__inspection"
            >
              <div className="product-workflow__inspection-heading">
                <div>
                  <p className="workbench-kicker">Runtime decision</p>
                  <h2 id={approvalInspectionTitleId}>Approval request</h2>
                </div>
                <Button onPress={() => closeInspection(onCloseApproval)} variant="quiet">
                  Close approval
                </Button>
              </div>
              <ApprovalWorkflow
                canResolve={canResolveApproval}
                client={client}
                reference={selectedApproval}
              />
            </section>
          ) : (
            <section
              aria-labelledby={approvalBlockedTitleId}
              className="product-workflow__blocked"
              role="alert"
            >
              <StatusBadge tone="critical">Scope mismatch</StatusBadge>
              <h2 id={approvalBlockedTitleId}>Approval review unavailable</h2>
              <p>
                The selected approval does not belong to the active invocation. Reopen it from the
                confirmed event timeline before reviewing the request.
              </p>
            </section>
          ))}
        {selectedArtifact &&
          (isArtifactSelectedForInvocation(selectedArtifact, reference) ? (
            <section
              aria-labelledby={presentation === "classic" ? artifactInspectionTitleId : undefined}
              aria-label={presentation === "workbench" ? "Artifact inspection" : undefined}
              className="product-workflow__inspection"
            >
              <div className="product-workflow__inspection-heading">
                {presentation === "classic" && (
                  <div>
                    <p className="workbench-kicker">Governed output</p>
                    <h2 id={artifactInspectionTitleId}>Artifact inspection</h2>
                  </div>
                )}
                <Button onPress={() => closeInspection(onCloseArtifact)} variant="quiet">
                  Close artifact
                </Button>
              </div>
              <ArtifactWorkflow
                compact={presentation === "workbench"}
                client={client}
                reference={selectedArtifact}
              />
            </section>
          ) : (
            <section
              aria-labelledby={artifactBlockedTitleId}
              className="product-workflow__blocked"
              role="alert"
            >
              <StatusBadge tone="critical">Scope mismatch</StatusBadge>
              <h2 id={artifactBlockedTitleId}>Artifact inspection unavailable</h2>
              <p>
                The selected artifact does not belong to the active invocation. Reopen it from the
                confirmed event timeline before inspecting the artifact.
              </p>
            </section>
          ))}
      </div>
    </section>
  );
  const activityView = (
    <section
      aria-label="Interaction activity"
      className="interaction-inspector__section"
      ref={activity}
      tabIndex={-1}
    >
      <EventTimelineWorkflow
        compact={presentation === "workbench"}
        onInspectEvent={
          presentation === "workbench"
            ? (event) => {
                if (
                  event.isolationDomainId !== reference.isolationDomainId ||
                  event.invocationId !== reference.invocationId
                )
                  return;
                beginInspection();
                setActivitySelection({ client, scope, event, artifactKey });
              }
            : undefined
        }
        client={client}
        onInspectQuestion={inspectQuestion}
        onInspectApproval={inspectApproval}
        onInspectArtifact={inspectArtifact}
        reference={reference}
      />
    </section>
  );
  const requestsView = (
    <section
      aria-label="Interaction questions and approvals"
      className="interaction-inspector__section"
      ref={requests}
      tabIndex={-1}
    >
      {inspectQuestion && (
        <QuestionDiscoveryWorkflow
          compact={presentation === "workbench"}
          client={client}
          reference={reference}
          onInspectQuestion={inspectQuestion}
        />
      )}
      <ApprovalDiscoveryWorkflow
        compact={presentation === "workbench"}
        client={client}
        reference={reference}
        onInspectApproval={inspectApproval}
      />
    </section>
  );
  const auditView = (
    <section
      aria-label="Interaction audit"
      className="interaction-inspector__section"
      ref={audit}
      tabIndex={-1}
    >
      <ResourceAuditWorkflow
        client={client}
        reference={{
          isolationDomainId: reference.isolationDomainId,
          resourceType: "invocation",
          resourceId: reference.invocationId,
        }}
      />
    </section>
  );

  if (presentation === "workbench")
    return (
      <div className="run-inspector">
        <SplitView
          primaryLabel="Run record"
          primary={
            <InvocationWorkflow
              compact
              client={client}
              reference={reference}
              canCancel={canCancelInvocation}
              disabledReason={cancellationDisabledReason}
              onInspectArtifact={inspectArtifact}
              renderDetails={({ result, files }) => (
                <section ref={navigation} tabIndex={-1} aria-label="Run views">
                  <Tabs
                    label="Run views"
                    selectedId={activeTab}
                    onSelectionChange={(value) => {
                      if (!isRunTab(value)) return;
                      setActiveTab(value);
                      onSelectTab?.(value);
                    }}
                    items={[
                      { id: "activity", label: "Activity", content: activityView },
                      { id: "requests", label: "Requests", content: requestsView },
                      {
                        id: "output",
                        label: "Output",
                        content: result ?? (
                          <p>Output is available after a confirmed successful run.</p>
                        ),
                      },
                      { id: "files", label: "Files", content: files },
                      { id: "audit", label: "Audit", content: auditView },
                    ]}
                  />
                </section>
              )}
            />
          }
          secondary={selection}
        />
      </div>
    );
  return (
    <div className="interaction-inspector">
      <nav
        aria-label="Interaction sections"
        className="interaction-inspector__navigation"
        ref={navigation}
        tabIndex={-1}
      >
        <Button variant="quiet" onPress={() => focusSection(overview.current)}>
          Overview and result
        </Button>
        <Button variant="quiet" onPress={() => focusSection(activity.current)}>
          Activity
        </Button>
        <Button variant="quiet" onPress={() => focusSection(requests.current)}>
          Questions and approvals
        </Button>
        <Button variant="quiet" onPress={() => focusSection(audit.current)}>
          Audit
        </Button>
        {hasSelection && (
          <Button variant="quiet" onPress={() => focusSection(inspection.current)}>
            Selected item
          </Button>
        )}
      </nav>
      {selection}
      <section
        aria-label="Interaction overview and result"
        className="interaction-inspector__section"
        ref={overview}
        tabIndex={-1}
      >
        <InvocationWorkflow
          canCancel={canCancelInvocation}
          client={client}
          disabledReason={cancellationDisabledReason}
          onInspectArtifact={inspectArtifact}
          reference={reference}
        />
      </section>
      {activityView}
      {requestsView}
      {auditView}
    </div>
  );
}
