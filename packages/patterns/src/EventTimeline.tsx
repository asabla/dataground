import { Button, StatusBadge, type StatusTone } from "@dataground/ui";
import { useId } from "react";

export interface TimelineEvent {
  actorId: string;
  correlationId: string;
  id: string;
  invocationId: string;
  isolationDomainId: string;
  occurredAt: string;
  payload: Record<string, unknown>;
  recordedAt: string;
  revisionId: string;
  schemaVersion: string;
  source?: "platform" | "runtime";
  sequence: number;
  serviceId: string;
  type: string;
}

export interface TimelineReference {
  invocationId: string;
  isolationDomainId: string;
}

export interface TimelineArtifactReference extends TimelineReference {
  artifactId: string;
}

export interface TimelineApprovalReference extends TimelineReference {
  approvalId: string;
}

export interface TimelineError {
  correlationId?: string;
  message: string;
  retryable: boolean;
}

export type TimelineConnectionState = "current" | "degraded" | "loading";

export interface EventTimelineProps {
  connectionState: TimelineConnectionState;
  error?: TimelineError;
  events: readonly TimelineEvent[];
  hiddenEventCount?: number;
  isReplaying?: boolean;
  onInspectApproval?: (reference: TimelineApprovalReference) => void;
  onInspectArtifact?: (reference: TimelineArtifactReference) => void;
  onReplay?: () => void;
  reference: TimelineReference;
}

interface EventPresentation {
  detail: string;
  label: string;
  tone: StatusTone;
}

const MAX_PREVIEW_LENGTH = 480;
const approvalIdPattern = /^apr_[0-9a-z]{20,32}$/u;
const artifactIdPattern = /^art_[0-9a-z]{20,32}$/u;

function replaceUnsafeControls(value: string): string {
  return Array.from(value, (character) => {
    const codePoint = character.codePointAt(0) ?? 0;
    return codePoint <= 8 ||
      codePoint === 11 ||
      codePoint === 12 ||
      (codePoint >= 14 && codePoint <= 31) ||
      (codePoint >= 127 && codePoint <= 159)
      ? "�"
      : character;
  }).join("");
}

function boundedText(value: unknown, fallback: string): string {
  if (typeof value !== "string" || value.trim() === "") {
    return fallback;
  }
  const normalized = replaceUnsafeControls(value).replaceAll(/\s+/gu, " ").trim();
  return normalized.length > MAX_PREVIEW_LENGTH
    ? `${normalized.slice(0, MAX_PREVIEW_LENGTH)}…`
    : normalized;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function boundedNumber(value: unknown): string | undefined {
  return typeof value === "number" && Number.isFinite(value) ? String(value) : undefined;
}

export function presentTimelineEvent(event: TimelineEvent): EventPresentation {
  if (event.source === "runtime" && event.type.startsWith("lifecycle.")) {
    switch (event.type) {
      case "lifecycle.started":
        return {
          label: "Runtime turn started",
          detail: "The runtime began processing a turn.",
          tone: "active",
        };
      case "lifecycle.waiting":
        return {
          label: "Runtime turn waiting",
          detail: "The runtime is waiting for an external response.",
          tone: "waiting",
        };
      case "lifecycle.succeeded":
        return {
          label: "Runtime turn completed",
          detail:
            "The runtime reported completion. Output validation and platform finalization may still be pending.",
          tone: "active",
        };
      case "lifecycle.failed":
        return {
          label: "Runtime turn failed",
          detail:
            "The runtime reported failure. Refresh invocation state for the platform outcome.",
          tone: "critical",
        };
      case "lifecycle.cancelled":
        return {
          label: "Runtime turn cancelled",
          detail:
            "The runtime reported cancellation. Platform cancellation may still be reconciling.",
          tone: "warning",
        };
      default:
        return {
          label: "Runtime lifecycle event",
          detail:
            "The runtime reported a lifecycle event. Refresh invocation state for the platform outcome.",
          tone: "neutral",
        };
    }
  }
  switch (event.type) {
    case "lifecycle.accepted":
      return {
        detail: "DataGround accepted the invocation for processing.",
        label: "Invocation accepted",
        tone: "active",
      };
    case "lifecycle.running":
      return {
        detail: "DataGround observed the invocation running.",
        label: "Invocation running",
        tone: "active",
      };
    case "lifecycle.cancellation.requested":
    case "lifecycle.cancellation-requested":
      return {
        detail: "Cancellation was requested. Processing may still be active.",
        label: "Cancellation requested",
        tone: "waiting",
      };
    case "lifecycle.cancelling":
      return {
        detail: "DataGround is reconciling cancellation.",
        label: "Invocation cancelling",
        tone: "waiting",
      };
    case "lifecycle.started":
      return {
        detail: boundedText(event.payload.message, "The runtime started processing."),
        label: "Invocation started",
        tone: "active",
      };
    case "lifecycle.waiting":
      return {
        detail: `Waiting for ${boundedText(event.payload.reason, "an external response")}.`,
        label: "Invocation waiting",
        tone: "waiting",
      };
    case "lifecycle.succeeded":
      return {
        detail: boundedText(event.payload.message, "The invocation completed successfully."),
        label: "Invocation succeeded",
        tone: "success",
      };
    case "lifecycle.failed":
      return {
        detail: `Failure code: ${boundedText(event.payload.code, "not reported")}.`,
        label: "Invocation failed",
        tone: "critical",
      };
    case "lifecycle.cancelled":
      return {
        detail: boundedText(event.payload.reason, "The invocation was cancelled."),
        label: "Invocation cancelled",
        tone: "warning",
      };
    case "output.text.delta":
      return {
        detail: boundedText(event.payload.text, "Text output was recorded without a preview."),
        label: "Text output",
        tone: "neutral",
      };
    case "activity.tool.started":
      return {
        detail: `Tool: ${boundedText(event.payload.name, "unnamed")}.`,
        label: "Tool started",
        tone: "active",
      };
    case "activity.tool.completed":
      return {
        detail: `Result: ${boundedText(event.payload.status, "completed")}.`,
        label: "Tool completed",
        tone: "success",
      };
    case "activity.process.started":
      return {
        detail: `Process: ${boundedText(event.payload.name, "unnamed")}.`,
        label: "Process started",
        tone: "active",
      };
    case "activity.process.completed": {
      const exitCode = boundedNumber(event.payload.exitCode);
      return {
        detail: exitCode === undefined ? "The process completed." : `Exit code: ${exitCode}.`,
        label: "Process completed",
        tone: exitCode === undefined || exitCode === "0" ? "success" : "critical",
      };
    }
    case "usage.recorded": {
      const total = boundedNumber(event.payload.totalTokens);
      return {
        detail: total === undefined ? "Usage was recorded." : `Total tokens: ${total}.`,
        label: "Usage recorded",
        tone: "neutral",
      };
    }
    case "interaction.approval.requested":
      return {
        detail: `Action: ${boundedText(event.payload.action, "not reported")}.`,
        label: "Approval requested",
        tone: "waiting",
      };
    case "interaction.approval.resolved":
      return {
        detail: `Decision: ${boundedText(event.payload.decision, "recorded")}.`,
        label: "Approval resolved",
        tone: "active",
      };
    case "interaction.question.requested":
      return {
        detail: boundedText(event.payload.prompt, "A response is required."),
        label: "Question requested",
        tone: "waiting",
      };
    case "artifact.available": {
      const descriptor = isRecord(event.payload.descriptor) ? event.payload.descriptor : undefined;
      return {
        detail: `Artifact: ${boundedText(
          descriptor?.name ?? event.payload.name ?? event.payload.artifactId,
          "metadata available",
        )}.`,
        label: "Artifact available",
        tone: "success",
      };
    }
    case "error.occurred":
      return {
        detail: boundedText(event.payload.message, "The runtime reported a safe error."),
        label: "Runtime error",
        tone: "critical",
      };
    default:
      return {
        detail:
          "This event type is not understood by this Workbench version. Its envelope remains preserved for replay.",
        label: "Unknown event",
        tone: "neutral",
      };
  }
}

export function timelineArtifactReference(
  event: TimelineEvent,
): TimelineArtifactReference | undefined {
  const artifactId = event.payload.artifactId;
  return event.type === "artifact.available" &&
    typeof artifactId === "string" &&
    artifactIdPattern.test(artifactId)
    ? {
        artifactId,
        invocationId: event.invocationId,
        isolationDomainId: event.isolationDomainId,
      }
    : undefined;
}

export function timelineApprovalReference(
  event: TimelineEvent,
): TimelineApprovalReference | undefined {
  const approvalId = event.payload.approvalId;
  return event.type === "interaction.approval.requested" &&
    typeof approvalId === "string" &&
    approvalIdPattern.test(approvalId)
    ? {
        approvalId,
        invocationId: event.invocationId,
        isolationDomainId: event.isolationDomainId,
      }
    : undefined;
}

const connectionPresentations: Record<
  TimelineConnectionState,
  { label: string; message: string; tone: StatusTone }
> = {
  current: {
    label: "Replay current",
    message: "All events through the displayed cursor have been loaded.",
    tone: "success",
  },
  degraded: {
    label: "Replay degraded",
    message: "Previously loaded events remain visible, but newer events could not be confirmed.",
    tone: "warning",
  },
  loading: {
    label: "Replaying events",
    message: "Retrieving ordered events after the last confirmed cursor.",
    tone: "active",
  },
};

export function EventTimeline({
  connectionState,
  error,
  events,
  hiddenEventCount = 0,
  isReplaying = false,
  onInspectApproval,
  onInspectArtifact,
  onReplay,
  reference,
}: EventTimelineProps) {
  const titleId = useId();
  const connection = connectionPresentations[connectionState];
  const cursor = events.at(-1)?.sequence ?? 0;

  return (
    <section
      aria-busy={isReplaying || undefined}
      aria-labelledby={titleId}
      className="dg-event-timeline"
    >
      <div className="dg-event-timeline__heading">
        <div>
          <p className="dg-event-timeline__eyebrow">Invocation journal</p>
          <h2 id={titleId}>Event timeline</h2>
        </div>
        <StatusBadge tone={connection.tone}>{connection.label}</StatusBadge>
      </div>

      <dl className="dg-event-timeline__scope">
        <div>
          <dt>Isolation domain</dt>
          <dd>{reference.isolationDomainId}</dd>
        </div>
        <div>
          <dt>Invocation</dt>
          <dd>{reference.invocationId}</dd>
        </div>
        <div>
          <dt>Confirmed cursor</dt>
          <dd>{cursor}</dd>
        </div>
      </dl>

      <p aria-live="polite" className="dg-event-timeline__connection">
        {connection.message}
      </p>

      {error && (
        <div className="dg-event-timeline__error" role="alert">
          <strong>Event replay not confirmed.</strong> {error.message}
          {error.correlationId && (
            <span>
              {" "}
              Correlation: <code>{error.correlationId}</code>
            </span>
          )}
        </div>
      )}

      {hiddenEventCount > 0 && (
        <p className="dg-event-timeline__notice">
          {hiddenEventCount} earlier {hiddenEventCount === 1 ? "event is" : "events are"} outside
          the bounded display. Replay continues from the confirmed cursor.
        </p>
      )}

      {events.length === 0 ? (
        <p className="dg-event-timeline__empty">
          {connectionState === "loading"
            ? "No events have been loaded yet."
            : "The invocation journal currently contains no events."}
        </p>
      ) : (
        <ol className="dg-event-timeline__list">
          {events.map((event) => {
            const presentation = presentTimelineEvent(event);
            const approvalReference = timelineApprovalReference(event);
            const artifactReference = timelineArtifactReference(event);
            return (
              <li key={event.id}>
                <article className="dg-event-timeline__event">
                  <div className="dg-event-timeline__event-heading">
                    <StatusBadge tone={presentation.tone}>{presentation.label}</StatusBadge>
                    <span className="dg-event-timeline__sequence">Sequence {event.sequence}</span>
                  </div>
                  <p>{presentation.detail}</p>
                  <dl className="dg-event-timeline__metadata">
                    <div>
                      <dt>Type</dt>
                      <dd>{boundedText(event.type, "Unknown")}</dd>
                    </div>
                    <div>
                      <dt>Occurred</dt>
                      <dd>
                        <time dateTime={event.occurredAt}>{event.occurredAt}</time>
                      </dd>
                    </div>
                    <div>
                      <dt>Actor</dt>
                      <dd>{event.actorId}</dd>
                    </div>
                  </dl>
                  {((approvalReference && onInspectApproval) ||
                    (artifactReference && onInspectArtifact)) && (
                    <div className="dg-event-timeline__event-actions">
                      {approvalReference && onInspectApproval && (
                        <Button
                          onPress={() => onInspectApproval(approvalReference)}
                          variant="quiet"
                        >
                          Review approval request
                        </Button>
                      )}
                      {artifactReference && onInspectArtifact && (
                        <Button
                          onPress={() => onInspectArtifact(artifactReference)}
                          variant="quiet"
                        >
                          Inspect artifact metadata
                        </Button>
                      )}
                    </div>
                  )}
                </article>
              </li>
            );
          })}
        </ol>
      )}

      {onReplay && (
        <div className="dg-event-timeline__actions">
          <Button isDisabled={isReplaying} onPress={onReplay} variant="secondary">
            {isReplaying
              ? "Replaying events…"
              : error?.retryable
                ? "Retry replay"
                : "Replay new events"}
          </Button>
        </div>
      )}
    </section>
  );
}
