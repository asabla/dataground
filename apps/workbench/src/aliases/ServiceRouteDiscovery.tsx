import { Button, DataTable, type DataTableSort, TextField } from "@dataground/ui";
import { useCallback, useEffect, useId, useReducer, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import {
  listServiceAliases,
  type ServiceAliasReadScope,
  type ServiceAliasResource,
} from "./aliasClient";
import { aliasDiscoveryReducer } from "./discovery";

export interface ServiceRouteDiscoveryProps {
  refreshGeneration?: number;
  revisionNumbers?: ReadonlyMap<string, number>;
  client: DataGroundClient;
  scope: ServiceAliasReadScope;
  canWithdraw?: boolean;
  onWithdraw: (alias: ServiceAliasResource) => void;
  onSelect?: (alias: ServiceAliasResource) => void;
}

export function ServiceRouteDiscovery({
  refreshGeneration = 0,
  revisionNumbers,
  client,
  scope: { isolationDomainId, serviceId },
  canWithdraw = false,
  onWithdraw,
  onSelect,
}: ServiceRouteDiscoveryProps) {
  const [sort, setSort] = useState<DataTableSort>({ column: "name", direction: "ascending" });
  const [query, setQuery] = useState("");
  const resourceScope = `${isolationDomainId}/${serviceId}`;
  const scope = `${resourceScope}/${refreshGeneration}`;
  const currentScope = useRef({ client, scope });
  currentScope.current = { client, scope };
  const searchScope = useRef({ client, resourceScope });
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
    if (
      searchScope.current.client !== client ||
      searchScope.current.resourceScope !== resourceScope
    ) {
      searchScope.current = { client, resourceScope };
      setQuery("");
    }
    void load();
    return () => {
      sequence.current++;
      inFlight.current = false;
      readTrigger.current = null;
    };
  }, [load, client, resourceScope]);
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
    if (
      currentScope.current.client === client &&
      currentScope.current.scope === scope &&
      !inFlight.current
    ) {
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
        <>
          <div className="resource-browser__toolbar">
            <TextField
              label="Search loaded routes"
              value={query}
              onChange={setQuery}
              type="search"
            />
            <p>{items.length} routes loaded.</p>
          </div>
          <DataTable
            sort={sort}
            onSortChange={setSort}
            label="Loaded service routes"
            items={items
              .filter((alias) => alias.name.toLocaleLowerCase().includes(query.toLocaleLowerCase()))
              .sort(
                (a, b) =>
                  (sort.direction === "ascending" ? 1 : -1) *
                  (sort.column === "name"
                    ? a.name.localeCompare(b.name)
                    : Date.parse(a.metadata.updatedAt) - Date.parse(b.metadata.updatedAt)),
              )}
            getRowId={(alias) => alias.metadata.id}
            getRowLabel={(alias) => alias.name}
            isLoading={loading}
            emptyMessage="No loaded routes match this search."
            columns={[
              {
                id: "name",
                title: "Route",
                allowsSorting: true,
                isRowHeader: true,
                render: (alias) => <strong>{alias.name}</strong>,
              },
              {
                id: "revision",
                title: "Version",
                render: (alias) =>
                  revisionNumbers?.has(alias.revisionId) ? (
                    `Revision ${revisionNumbers.get(alias.revisionId)}`
                  ) : (
                    <details>
                      <summary>Revision reference</summary>
                      <code>{alias.revisionId}</code>
                    </details>
                  ),
              },
              {
                id: "updated",
                title: "Route updated",
                allowsSorting: true,
                render: (alias) => (
                  <time dateTime={alias.metadata.updatedAt}>
                    {new Date(alias.metadata.updatedAt).toLocaleString()}
                  </time>
                ),
              },
              {
                id: "actions",
                title: "Actions",
                render: (alias) => (
                  <div className="resource-browser__actions">
                    {onSelect && (
                      <Button
                        aria-label={`Use ${alias.name} route`}
                        isDisabled={loading || !!error}
                        variant="quiet"
                        onPress={() => {
                          if (
                            current &&
                            currentScope.current.client === client &&
                            currentScope.current.scope === scope &&
                            !inFlight.current &&
                            !loading &&
                            !error
                          )
                            onSelect({ ...alias, metadata: { ...alias.metadata } });
                        }}
                      >
                        Use route
                      </Button>
                    )}
                    {canWithdraw && (
                      <Button
                        aria-label={`Withdraw ${alias.name} alias`}
                        isDisabled={loading || !!error}
                        variant="quiet"
                        onPress={() => {
                          if (
                            current &&
                            currentScope.current.client === client &&
                            currentScope.current.scope === scope &&
                            !inFlight.current &&
                            !loading &&
                            !error &&
                            canWithdraw
                          )
                            onWithdraw({ ...alias, metadata: { ...alias.metadata } });
                        }}
                      >
                        Withdraw
                      </Button>
                    )}
                  </div>
                ),
              },
            ]}
          />
        </>
      )}
      {current && state.nextCursor && !error && (
        <Button aria-disabled={loading} onPress={() => read(state.nextCursor)} variant="quiet">
          Load more routes
        </Button>
      )}
    </section>
  );
}
