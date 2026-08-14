import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import type { InvocationEvent } from "./client";
import {
  EventTimelineWorkflow,
  eventReferenceKey,
  eventTimelineReducer,
  mergeEventReplay,
} from "./EventTimelineWorkflow";

const reference = {
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
};
const referenceKey = eventReferenceKey(reference);

function event(sequence: number): InvocationEvent {
  return {
    actorId: "reference-runtime",
    correlationId: "correlation-fixture",
    id: `evt_${sequence.toString(36).padStart(20, "0")}`,
    invocationId: reference.invocationId,
    isolationDomainId: reference.isolationDomainId,
    occurredAt: "2026-08-14T12:00:00Z",
    payload: { message: "Reference runtime started." },
    recordedAt: "2026-08-14T12:00:00.001Z",
    revisionId: "rev_00000000000000000001",
    schemaVersion: "dataground.event/v1",
    sequence,
    serviceId: "svc_00000000000000000001",
    type: "lifecycle.started",
  };
}

describe("EventTimelineWorkflow", () => {
  it("renders a bounded loading state before the authoritative replay completes", () => {
    const markup = renderToStaticMarkup(
      <EventTimelineWorkflow client={{} as DataGroundClient} reference={reference} />,
    );

    assert.match(markup, /Replaying events/u);
    assert.match(markup, /No events have been loaded yet/u);
    assert.match(markup, new RegExp(reference.isolationDomainId, "u"));
  });

  it("merges contiguous replay and treats exact duplicates as read-only", () => {
    const first = mergeEventReplay(
      { cursor: 0, events: [], hiddenEventCount: 0 },
      { cursor: 2, events: [event(1), event(2)], ok: true },
    );
    const duplicate = { ...event(2), payload: { detail: "stable", message: "same" } };
    const reorderedDuplicate = {
      ...duplicate,
      payload: { message: "same", detail: "stable" },
    };
    const replayed = mergeEventReplay(
      { ...first, events: [event(1), duplicate] },
      {
        cursor: 2,
        events: [reorderedDuplicate],
        ok: true,
      },
    );

    assert.equal(first.error, undefined);
    assert.equal(replayed.error, undefined);
    assert.equal(replayed.cursor, 2);
    assert.equal(replayed.events.length, 2);
  });

  it("rejects conflicting replay and sequence gaps without replacing confirmed events", () => {
    const current = { cursor: 1, events: [event(1)], hiddenEventCount: 0 };
    const conflict = mergeEventReplay(current, {
      cursor: 1,
      events: [{ ...event(1), type: "lifecycle.failed" }],
      ok: true,
    });
    const gap = mergeEventReplay(current, {
      cursor: 3,
      events: [event(3)],
      ok: true,
    });

    assert.equal(conflict.error?.code, "WORKBENCH_EVENT_REPLAY_CONFLICT");
    assert.equal(conflict.events, current.events);
    assert.equal(gap.error?.code, "WORKBENCH_EVENT_SEQUENCE_GAP");
    assert.equal(gap.events, current.events);
  });

  it("keeps confirmed events visible when a later replay fails", () => {
    const current = {
      cursor: 1,
      events: [event(1)],
      hiddenEventCount: 0,
      loading: true,
      referenceKey,
    };
    const state = eventTimelineReducer(current, {
      referenceKey,
      result: {
        error: {
          code: "WORKBENCH_NETWORK_UNAVAILABLE",
          message: "The Workbench could not reach DataGround.",
          retryable: true,
        },
        ok: false,
      },
      type: "replay-finished",
    });

    assert.equal(state.events.length, 1);
    assert.equal(state.cursor, 1);
    assert.equal(state.error?.code, "WORKBENCH_NETWORK_UNAVAILABLE");
    assert.equal(state.loading, false);
  });

  it("clears prior scope immediately when a different invocation starts loading", () => {
    const state = eventTimelineReducer(
      {
        cursor: 1,
        events: [event(1)],
        hiddenEventCount: 0,
        loading: false,
        referenceKey,
      },
      {
        afterSequence: 0,
        referenceKey: "iso_00000000000000000001:inv_00000000000000000002",
        type: "replay-started",
      },
    );

    assert.equal(state.events.length, 0);
    assert.equal(state.cursor, 0);
    assert.equal(state.loading, true);
  });

  it("ignores a late completion from a prior invocation scope", () => {
    const state = {
      cursor: 0,
      events: [],
      hiddenEventCount: 0,
      loading: true,
      referenceKey: "iso_00000000000000000001:inv_00000000000000000002",
    };
    const completed = eventTimelineReducer(state, {
      referenceKey,
      result: { cursor: 1, events: [event(1)], ok: true },
      type: "replay-finished",
    });

    assert.equal(completed, state);
  });

  it("bounds visible events while retaining the authoritative cursor", () => {
    const events = Array.from({ length: 205 }, (_, index) => event(index + 1));
    const merged = mergeEventReplay(
      { cursor: 0, events: [], hiddenEventCount: 0 },
      { cursor: 205, events, ok: true },
    );

    assert.equal(merged.events.length, 200);
    assert.equal(merged.events[0]?.sequence, 6);
    assert.equal(merged.hiddenEventCount, 5);
    assert.equal(merged.cursor, 205);
  });
});
