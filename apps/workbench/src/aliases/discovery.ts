import type { DataGroundClient } from "../contracts/client";
import type {
  ServiceAliasFailure,
  ServiceAliasListResult,
  ServiceAliasResource,
} from "./aliasClient";

export interface AliasDiscoveryState {
  client: DataGroundClient;
  scope: string;
  requestId: number;
  loading: boolean;
  items: ServiceAliasResource[];
  nextCursor?: string;
  requestCursor?: string;
  seenCursors: string[];
  error?: ServiceAliasFailure;
}

type AliasDiscoveryAction =
  | {
      type: "requested";
      client: DataGroundClient;
      scope: string;
      requestId: number;
      cursor?: string;
    }
  | { type: "received"; requestId: number; result: ServiceAliasListResult };

export function aliasDiscoveryReducer(
  state: AliasDiscoveryState,
  action: AliasDiscoveryAction,
): AliasDiscoveryState {
  if (action.type === "requested") {
    const continuing =
      action.cursor !== undefined && state.client === action.client && state.scope === action.scope;
    return {
      client: action.client,
      scope: action.scope,
      requestId: action.requestId,
      loading: true,
      items: continuing ? state.items : [],
      requestCursor: action.cursor,
      nextCursor: continuing ? state.nextCursor : undefined,
      seenCursors: continuing ? state.seenCursors : [],
    };
  }
  if (action.requestId !== state.requestId || !state.loading) return state;
  if (!action.result.ok) {
    // A denied read invalidates the visible route set, including earlier pages.
    const denied = action.result.error.status === 401 || action.result.error.status === 403;
    return {
      ...state,
      loading: false,
      error: action.result.error,
      ...(denied ? { items: [], nextCursor: undefined } : {}),
    };
  }
  const { items, nextCursor } = action.result.page;
  const previous = state.items.at(-1);
  const first = items[0];
  if (
    (nextCursor !== undefined && state.seenCursors.includes(nextCursor)) ||
    (previous && first && previous.name >= first.name) ||
    items.some((item) => state.items.some((existing) => existing.metadata.id === item.metadata.id))
  ) {
    return {
      ...state,
      loading: false,
      nextCursor: undefined,
      error: {
        code: "WORKBENCH_ALIAS_DISCOVERY_STALLED",
        message: "Route discovery did not advance. Refresh routes to recover.",
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
