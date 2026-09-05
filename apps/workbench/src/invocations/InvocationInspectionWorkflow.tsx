import { Button, StatusBadge } from "@dataground/ui";
import { useId } from "react";
import { ApprovalWorkflow, type InvocationApprovalReference } from "../approvals";
import { ArtifactWorkflow, type InvocationArtifactReference } from "../artifacts";
import type { DataGroundClient } from "../contracts/client";
import { EventTimelineWorkflow } from "../events";
import { type InvocationQuestionReference, QuestionWorkflow } from "../questions";
import type { InvocationReference } from "./client";
import { InvocationWorkflow } from "./InvocationWorkflow";

const isolationDomainIdPattern = /^iso_[0-9a-z]{20,32}$/u;
const invocationIdPattern = /^inv_[0-9a-z]{20,32}$/u;
const approvalIdPattern = /^apr_[0-9a-z]{20,32}$/u;
const artifactIdPattern = /^art_[0-9a-z]{20,32}$/u;

export interface InvocationInspectionWorkflowProps {
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

export function InvocationInspectionWorkflow({
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
  const approvalBlockedTitleId = useId();
  const approvalInspectionTitleId = useId();
  const artifactBlockedTitleId = useId();
  const artifactInspectionTitleId = useId();
  return (
    <>
      <InvocationWorkflow
        canCancel={canCancelInvocation}
        client={client}
        disabledReason={cancellationDisabledReason}
        onInspectArtifact={onInspectArtifact}
        reference={reference}
      />
      <EventTimelineWorkflow
        client={client}
        onInspectQuestion={onInspectQuestion}
        onInspectApproval={onInspectApproval}
        onInspectArtifact={onInspectArtifact}
        reference={reference}
      />
      {selectedQuestion &&
        (isQuestionSelectedForInvocation(selectedQuestion, reference) ? (
          <section className="product-workflow__inspection" aria-label="Question inspection">
            <Button onPress={onCloseQuestion} variant="quiet">
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
              confirmed event timeline.
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
              <Button onPress={onCloseApproval} variant="quiet">
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
            aria-labelledby={artifactInspectionTitleId}
            className="product-workflow__inspection"
          >
            <div className="product-workflow__inspection-heading">
              <div>
                <p className="workbench-kicker">Governed output</p>
                <h2 id={artifactInspectionTitleId}>Artifact inspection</h2>
              </div>
              <Button onPress={onCloseArtifact} variant="quiet">
                Close metadata
              </Button>
            </div>
            <ArtifactWorkflow client={client} reference={selectedArtifact} />
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
              confirmed event timeline before inspecting metadata.
            </p>
          </section>
        ))}
    </>
  );
}
