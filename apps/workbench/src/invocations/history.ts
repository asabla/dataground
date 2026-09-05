import type { DataGroundClient } from "../contracts/client";
import type { InvocationFailure, InvocationListResult, InvocationSummaryResource } from "./client";

export interface InvocationHistoryState {
  client: DataGroundClient;
  scope: string;
  requestId: number;
  loading: boolean;
  items: InvocationSummaryResource[];
  nextCursor?: string;
  requestCursor?: string;
  seenCursors: string[];
  error?: InvocationFailure;
}

export type InvocationHistoryAction =
  | {
      type: "requested";
      client: DataGroundClient;
      scope: string;
      requestId: number;
      cursor?: string;
    }
  | { type: "received"; requestId: number; result: InvocationListResult };

export function invocationHistoryReducer(
  state: InvocationHistoryState,
  action: InvocationHistoryAction,
): InvocationHistoryState {
  if (action.type === "requested") {
    const continuing =
      action.cursor !== undefined && state.scope === action.scope && state.client === action.client;
    return {
      client: action.client,
      scope: action.scope,
      requestId: action.requestId,
      loading: true,
      items: continuing ? state.items : [],
      nextCursor: continuing ? state.nextCursor : undefined,
      requestCursor: action.cursor,
      seenCursors: continuing ? state.seenCursors : [],
    };
  }
  if (action.requestId !== state.requestId || !state.loading) return state;
  if (!action.result.ok) {
    const denied = action.result.error.status === 401 || action.result.error.status === 403;
    return {
      ...state,
      loading: false,
      error: action.result.error,
      ...(denied ? { items: [], nextCursor: undefined } : {}),
    };
  }
  const { items, nextCursor } = action.result.page;
  if (
    (nextCursor !== undefined && state.seenCursors.includes(nextCursor)) ||
    items.some((item) => state.items.some((existing) => existing.metadata.id === item.metadata.id))
  ) {
    return {
      ...state,
      loading: false,
      nextCursor: undefined,
      error: {
        code: "WORKBENCH_INVOCATION_HISTORY_STALLED",
        message: "Invocation history did not advance. Refresh history to recover.",
        retryable: false,
      },
    };
  }
  return {
    ...state,
    loading: false,
    error: undefined,
    items: [...state.items, ...items],
    nextCursor,
    seenCursors: nextCursor === undefined ? state.seenCursors : [...state.seenCursors, nextCursor],
  };
}
