import { EventTimeline, type TimelineEvent } from "@dataground/patterns";
import "@dataground/patterns/styles.css";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import {
  type EventReplayFailure,
  type EventReplayResult,
  type InvocationEvent,
  type InvocationEventReference,
  replayInvocationEvents,
} from "./client";

const MAX_VISIBLE_EVENTS = 200;

interface EventTimelineState {
  cursor: number;
  error?: EventReplayFailure;
  events: InvocationEvent[];
  hiddenEventCount: number;
  loading: boolean;
  referenceKey: string;
}

type EventTimelineAction =
  | { afterSequence: number; referenceKey: string; type: "replay-started" }
  | { referenceKey: string; result: EventReplayResult; type: "replay-finished" };

export interface EventTimelineWorkflowProps {
  client: DataGroundClient;
  reference: InvocationEventReference;
}

export interface EventMergeResult {
  cursor: number;
  error?: EventReplayFailure;
  events: InvocationEvent[];
  hiddenEventCount: number;
}

function canonicalize(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map(canonicalize);
  }
  if (value !== null && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value)
        .sort(([left], [right]) => left.localeCompare(right))
        .map(([key, entry]) => [key, canonicalize(entry)]),
    );
  }
  return value;
}

function sameEvent(left: InvocationEvent, right: InvocationEvent): boolean {
  return JSON.stringify(canonicalize(left)) === JSON.stringify(canonicalize(right));
}

export function mergeEventReplay(
  current: Pick<EventTimelineState, "cursor" | "events" | "hiddenEventCount">,
  replay: Extract<EventReplayResult, { ok: true }>,
): EventMergeResult {
  const events = [...current.events];
  let cursor = current.cursor;
  for (const event of replay.events) {
    if (event.sequence <= cursor) {
      const existing = events.find(
        (value) => value.sequence === event.sequence || value.id === event.id,
      );
      if (!existing || !sameEvent(existing, event)) {
        return {
          ...current,
          error: {
            code: "WORKBENCH_EVENT_REPLAY_CONFLICT",
            message: "A replayed event conflicts with the previously confirmed journal.",
            retryable: false,
          },
        };
      }
      continue;
    }
    if (event.sequence !== cursor + 1 || events.some((value) => value.id === event.id)) {
      return {
        ...current,
        error: {
          code: "WORKBENCH_EVENT_SEQUENCE_GAP",
          message: "The replay did not continue from the confirmed event cursor.",
          retryable: true,
        },
      };
    }
    events.push(event);
    cursor = event.sequence;
  }
  if (replay.cursor !== cursor) {
    return {
      ...current,
      error: {
        code: "WORKBENCH_EVENT_CURSOR_MISMATCH",
        message: "The replay cursor did not match the confirmed event journal.",
        retryable: true,
      },
    };
  }
  const overflow = Math.max(0, events.length - MAX_VISIBLE_EVENTS);
  return {
    cursor,
    events: overflow === 0 ? events : events.slice(overflow),
    hiddenEventCount: current.hiddenEventCount + overflow,
  };
}

export function eventReferenceKey(reference: InvocationEventReference): string {
  return `${reference.isolationDomainId}:${reference.invocationId}`;
}

export function eventTimelineReducer(
  state: EventTimelineState,
  action: EventTimelineAction,
): EventTimelineState {
  switch (action.type) {
    case "replay-started":
      if (state.referenceKey !== action.referenceKey || action.afterSequence === 0) {
        return {
          cursor: 0,
          events: [],
          hiddenEventCount: 0,
          loading: true,
          referenceKey: action.referenceKey,
        };
      }
      return { ...state, error: undefined, loading: true };
    case "replay-finished": {
      if (state.referenceKey !== action.referenceKey) {
        return state;
      }
      if (!action.result.ok) {
        return { ...state, error: action.result.error, loading: false };
      }
      const merged = mergeEventReplay(state, action.result);
      return {
        cursor: merged.cursor,
        error: merged.error,
        events: merged.events,
        hiddenEventCount: merged.hiddenEventCount,
        loading: false,
        referenceKey: state.referenceKey,
      };
    }
  }
}

export function EventTimelineWorkflow({ client, reference }: EventTimelineWorkflowProps) {
  const currentReferenceKey = eventReferenceKey(reference);
  const [state, setState] = useState<EventTimelineState>({
    cursor: 0,
    events: [],
    hiddenEventCount: 0,
    loading: true,
    referenceKey: currentReferenceKey,
  });
  const requestGeneration = useRef(0);
  const stableReference = useMemo(
    () => ({
      invocationId: reference.invocationId,
      isolationDomainId: reference.isolationDomainId,
    }),
    [reference.invocationId, reference.isolationDomainId],
  );

  const dispatch = useCallback((action: EventTimelineAction) => {
    setState((current) => eventTimelineReducer(current, action));
  }, []);

  const loadReplay = useCallback(
    async (afterSequence: number) => {
      const generation = ++requestGeneration.current;
      const referenceKey = eventReferenceKey(stableReference);
      dispatch({ afterSequence, referenceKey, type: "replay-started" });
      const result = await replayInvocationEvents(client, stableReference, afterSequence);
      if (requestGeneration.current === generation) {
        dispatch({ referenceKey, result, type: "replay-finished" });
      }
    },
    [client, dispatch, stableReference],
  );

  useEffect(() => {
    void loadReplay(0);
    return () => {
      requestGeneration.current++;
    };
  }, [loadReplay]);

  const stateMatchesReference = state.referenceKey === currentReferenceKey;
  const visibleEvents = stateMatchesReference ? state.events : [];
  const connectionState =
    !stateMatchesReference || state.loading ? "loading" : state.error ? "degraded" : "current";

  return (
    <EventTimeline
      connectionState={connectionState}
      error={stateMatchesReference ? state.error : undefined}
      events={visibleEvents as TimelineEvent[]}
      hiddenEventCount={stateMatchesReference ? state.hiddenEventCount : 0}
      isReplaying={!stateMatchesReference || state.loading}
      onReplay={() => void loadReplay(stateMatchesReference ? state.cursor : 0)}
      reference={reference}
    />
  );
}
