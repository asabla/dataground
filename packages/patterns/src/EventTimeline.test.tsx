import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import {
  EventTimeline,
  presentTimelineEvent,
  type TimelineEvent,
  timelineArtifactReference,
} from "./EventTimeline";

const baseEvent: TimelineEvent = {
  actorId: "reference-runtime",
  correlationId: "correlation-fixture",
  id: "evt_00000000000000000001",
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
  occurredAt: "2026-08-14T12:00:00Z",
  payload: { message: "Reference runtime started." },
  recordedAt: "2026-08-14T12:00:00.001Z",
  revisionId: "rev_00000000000000000001",
  schemaVersion: "dataground.event/v1",
  sequence: 1,
  serviceId: "svc_00000000000000000001",
  type: "lifecycle.started",
};

const reference = {
  invocationId: baseEvent.invocationId,
  isolationDomainId: baseEvent.isolationDomainId,
};

describe("EventTimeline", () => {
  it("renders ordered event scope, cursor, type, and safe presentation", () => {
    const markup = renderToStaticMarkup(
      <EventTimeline connectionState="current" events={[baseEvent]} reference={reference} />,
    );

    assert.match(markup, /Event timeline/u);
    assert.match(markup, /Replay current/u);
    assert.match(markup, /Confirmed cursor/u);
    assert.match(markup, /Sequence 1/u);
    assert.match(markup, /Invocation started/u);
    assert.match(markup, /Reference runtime started/u);
    assert.match(markup, /reference-runtime/u);
  });

  it("preserves unknown event visibility without rendering arbitrary payload data", () => {
    const unknown = {
      ...baseEvent,
      payload: { secretNativePayload: "must-not-render" },
      type: "runtime.future.signal",
    };
    const markup = renderToStaticMarkup(
      <EventTimeline connectionState="current" events={[unknown]} reference={reference} />,
    );

    assert.match(markup, /Unknown event/u);
    assert.match(markup, /runtime.future.signal/u);
    assert.match(markup, /remains preserved for replay/u);
    assert.doesNotMatch(markup, /must-not-render/u);
  });

  it("keeps prior events visible while replay is degraded", () => {
    const markup = renderToStaticMarkup(
      <EventTimeline
        connectionState="degraded"
        error={{
          correlationId: "cor_00000000000000000001",
          message: "The event service is unavailable.",
          retryable: true,
        }}
        events={[baseEvent]}
        hiddenEventCount={4}
        onReplay={() => undefined}
        reference={reference}
      />,
    );

    assert.match(markup, /Previously loaded events remain visible/u);
    assert.match(markup, /Event replay not confirmed/u);
    assert.match(markup, /4 earlier events are outside/u);
    assert.match(markup, /Retry replay/u);
    assert.match(markup, /Invocation started/u);
  });

  it("renders explicit loading and empty states without announcing individual events", () => {
    const markup = renderToStaticMarkup(
      <EventTimeline connectionState="loading" events={[]} isReplaying reference={reference} />,
    );

    assert.match(markup, /Replaying events/u);
    assert.match(markup, /No events have been loaded yet/u);
    assert.match(markup, /aria-live="polite"/u);
    assert.doesNotMatch(markup, /role="log"/u);
  });

  it("bounds text previews and treats nonzero process exits as critical", () => {
    const text = presentTimelineEvent({
      ...baseEvent,
      payload: { text: "x".repeat(700) },
      type: "output.text.delta",
    });
    const process = presentTimelineEvent({
      ...baseEvent,
      payload: { exitCode: 7 },
      type: "activity.process.completed",
    });

    assert.equal(text.detail.length, 481);
    assert.match(text.detail, /…$/u);
    assert.equal(process.tone, "critical");
    assert.equal(process.detail, "Exit code: 7.");
  });

  it("neutralizes terminal controls and reads artifact names from normalized descriptors", () => {
    const output = presentTimelineEvent({
      ...baseEvent,
      payload: { text: "safe\u001b[31m text" },
      type: "output.text.delta",
    });
    const artifact = presentTimelineEvent({
      ...baseEvent,
      payload: { artifactId: "art_00000000000000000001", descriptor: { name: "report.json" } },
      type: "artifact.available",
    });

    assert.equal(output.detail, "safe�[31m text");
    assert.equal(artifact.detail, "Artifact: report.json.");
  });

  it("offers artifact inspection only for a governed artifact reference", () => {
    const artifactEvent = {
      ...baseEvent,
      payload: {
        artifactId: "art_00000000000000000001",
        descriptor: { name: "report.json" },
      },
      type: "artifact.available",
    };
    const malformedEvent = {
      ...artifactEvent,
      payload: { artifactId: "native-object-key" },
    };
    const markup = renderToStaticMarkup(
      <EventTimeline
        connectionState="current"
        events={[artifactEvent]}
        onInspectArtifact={() => undefined}
        reference={reference}
      />,
    );

    assert.match(markup, /Inspect artifact metadata/u);
    assert.equal(timelineArtifactReference(artifactEvent)?.artifactId, "art_00000000000000000001");
    assert.equal(timelineArtifactReference(malformedEvent), undefined);
  });
});
