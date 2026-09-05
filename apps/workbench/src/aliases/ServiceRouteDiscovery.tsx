import { Button } from "@dataground/ui";
import { useCallback, useEffect, useId, useReducer, useRef } from "react";
import type { DataGroundClient } from "../contracts/client";
import {
  listServiceAliases,
  type ServiceAliasReadScope,
  type ServiceAliasResource,
} from "./aliasClient";
import { aliasDiscoveryReducer } from "./discovery";

export interface ServiceRouteDiscoveryProps {
  client: DataGroundClient;
  scope: ServiceAliasReadScope;
  canWithdraw?: boolean;
  onWithdraw: (alias: ServiceAliasResource) => void;
  onSelect?: (alias: ServiceAliasResource) => void;
}

export function ServiceRouteDiscovery({
  client,
  scope: { isolationDomainId, serviceId },
  canWithdraw = false,
  onWithdraw,
  onSelect,
}: ServiceRouteDiscoveryProps) {
  const scope = `${isolationDomainId}/${serviceId}`;
  const titleId = useId();
  const sequence = useRef(0);
  const inFlight = useRef(false);
  const readTrigger = useRef<Element | null>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const [state, dispatch] = useReducer(aliasDiscoveryReducer, {
    client,
    scope,
    requestId: 0,
    loading: true,
    items: [],
    seenCursors: [],
  });
  const load = useCallback(
    async (cursor?: string) => {
      const requestId = ++sequence.current;
      inFlight.current = true;
      dispatch({ type: "requested", client, scope, requestId, cursor });
      const result = await listServiceAliases(client, { isolationDomainId, serviceId }, cursor);
      if (sequence.current === requestId) {
        inFlight.current = false;
        dispatch({ type: "received", requestId, result });
      }
    },
    [client, isolationDomainId, serviceId, scope],
  );
  useEffect(() => {
    void load();
    return () => {
      sequence.current++;
      inFlight.current = false;
      readTrigger.current = null;
    };
  }, [load]);
  // Connection and scope changes hide prior rows before effects or late reads run.
  const current = state.client === client && state.scope === scope;
  const items = current ? state.items : [];
  const loading = !current || state.loading;
  const error = current ? state.error : undefined;
  useEffect(() => {
    if (loading || !current) return;
    const trigger = readTrigger.current;
    readTrigger.current = null;
    if (trigger && !trigger.isConnected && document.activeElement === document.body)
      heading.current?.focus();
  }, [loading, current]);
  const read = (cursor?: string) => {
    if (!inFlight.current) {
      readTrigger.current = document.activeElement;
      void load(cursor);
    }
  };
  return (
    <section aria-labelledby={titleId} className="product-workflow__inspection">
      <div className="workbench-page-heading workbench-page-heading--compact">
        <div>
          <h2 id={titleId} ref={heading} tabIndex={-1}>
            Service routes
          </h2>
          <p>Active aliases and their published revisions. Refresh to reconcile routing changes.</p>
        </div>
        <Button aria-disabled={loading} onPress={() => read()} variant="quiet">
          Refresh routes
        </Button>
      </div>
      {loading && <p role="status">Loading service routes…</p>}
      {error && (
        <div role="alert" className="workbench-inline-error">
          <p>{error.message}</p>
          {error.retryable && (
            <Button
              aria-disabled={loading}
              onPress={() => read(state.requestCursor)}
              variant="quiet"
            >
              Retry route listing
            </Button>
          )}
        </div>
      )}
      {!loading && !error && items.length === 0 && <p>No active routes for this service.</p>}
      {items.length > 0 && (
        <ul className="invocation-history-list">
          {items.map((alias) => (
            <li key={alias.metadata.id}>
              <p>
                Alias <strong>{alias.name}</strong> · Version {alias.metadata.version}
              </p>
              <p>
                Revision <code>{alias.revisionId}</code>
              </p>
              {onSelect && (
                <Button
                  isDisabled={loading || !!error}
                  onPress={() => {
                    if (current && !inFlight.current && !loading && !error)
                      onSelect({ ...alias, metadata: { ...alias.metadata } });
                  }}
                  variant="quiet"
                >
                  Use {alias.name} route
                </Button>
              )}
              {canWithdraw && (
                <Button
                  isDisabled={loading || !!error}
                  onPress={() => {
                    if (current && !inFlight.current && !loading && !error && canWithdraw)
                      onWithdraw({ ...alias, metadata: { ...alias.metadata } });
                  }}
                  variant="quiet"
                >
                  Withdraw {alias.name} alias
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {current && state.nextCursor && !error && (
        <Button aria-disabled={loading} onPress={() => read(state.nextCursor)} variant="quiet">
          Load more routes
        </Button>
      )}
    </section>
  );
}
