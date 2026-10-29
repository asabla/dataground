import { Button, DataTable, StatusBadge } from "@dataground/ui";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import { useDiscoveryPage } from "../discovery/useDiscoveryPage";
import { type SearchResult, searchKindLabels } from "../search/client";
import type { AgentServiceFailure } from "../services/client";
import {
  listSavedResources,
  type SavedResource,
  type SavedResourceRequest,
  setSavedResource,
} from "./client";

interface Attempt {
  key: string;
  request: SavedResourceRequest;
  name: string;
  uncertain: boolean;
}
export function useSavedResources(client: DataGroundClient, scope: string, enabled: boolean) {
  const [revision, setRevision] = useState(0);
  // A confirmed command invalidates a list read already in flight, including
  // when the user navigated away before the command response arrived.
  const source = useMemo(
    () => ({ client, scope, enabled, revision }),
    [client, scope, enabled, revision],
  );
  const read = useCallback(
    (cursor?: string) =>
      source.enabled
        ? listSavedResources(source.client, source.scope, cursor)
        : Promise.resolve({ ok: true as const, page: { items: [] as SavedResource[] } }),
    [source],
  );
  const page = useDiscoveryPage(
    read,
    "Saved resources did not advance. Refresh the list to recover.",
  );
  const [attempt, setAttempt] = useState<Attempt>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<AgentServiceFailure>();
  const [confirmation, setConfirmation] = useState("");
  const [focusLocation, setFocusLocation] = useState<string>();
  const busy = useRef(false),
    generation = useRef(0),
    current = useRef({ client, scope });
  current.current = { client, scope };
  useEffect(
    () => () => {
      generation.current++;
    },
    [],
  );
  const submit = async (next: Attempt) => {
    if (busy.current) return;
    busy.current = true;
    const request = ++generation.current;
    setFocusLocation(window.location.hash);
    setAttempt(next);
    setPending(true);
    setError(undefined);
    setConfirmation("");
    const result = await setSavedResource(client, scope, next.request, next.key);
    if (
      request !== generation.current ||
      current.current.client !== client ||
      current.current.scope !== scope
    )
      return;
    busy.current = false;
    setPending(false);
    if (result.ok) {
      setAttempt(undefined);
      setConfirmation(
        `${next.request.saved ? "Save" : "Removal"} request confirmed for ${searchKindLabels[next.request.kind].toLowerCase()} ${next.name}.`,
      );
      setRevision((v) => v + 1);
    } else {
      const uncertain = next.uncertain || !!result.error.outcomeUnknown;
      setError({ ...result.error, ...(uncertain ? { outcomeUnknown: true } : {}) });
      if (uncertain || result.error.retryable) setAttempt({ ...next, uncertain });
      else setAttempt(undefined);
    }
  };
  const change = (
    kind: SavedResourceRequest["kind"],
    resourceId: string,
    name: string,
    saved: boolean,
  ) => {
    if (busy.current || attempt) return;
    try {
      void submit({
        key: `saved:${crypto.randomUUID().replaceAll("-", "")}`,
        request: { kind, resourceId, saved },
        name,
        uncertain: false,
      });
    } catch {
      setError({
        code: "WORKBENCH_SECURE_RANDOM_UNAVAILABLE",
        message: "A secure request identifier could not be created. Try again before saving.",
        retryable: false,
      });
    }
  };
  return {
    page,
    pending,
    error,
    confirmation,
    focusLocation,
    attempt,
    blocked: pending || !!attempt,
    change,
    retry: () => {
      if (attempt) void submit(attempt);
    },
  };
}
export type SavedResourcesController = ReturnType<typeof useSavedResources>;

export function SavedResourceNotice({ controller }: { controller: SavedResourcesController }) {
  const notice = useRef<HTMLElement>(null);
  const confirmation =
    typeof window !== "undefined" && controller.focusLocation === window.location.hash
      ? controller.confirmation
      : "";
  useEffect(() => {
    if (
      !controller.pending &&
      (controller.error || controller.confirmation) &&
      controller.focusLocation === window.location.hash &&
      document.activeElement === document.body
    )
      notice.current?.focus();
  }, [controller.pending, controller.error, controller.confirmation, controller.focusLocation]);
  if (!controller.pending && !controller.error && !confirmation) return null;
  return (
    <section
      ref={notice}
      tabIndex={-1}
      className="workbench-command-notice"
      aria-label="Saved resource request"
    >
      {controller.pending && (
        <p role="status">
          {controller.attempt?.request.saved ? "Saving" : "Removing"} {controller.attempt?.name}…
        </p>
      )}
      {controller.error && <p role="alert">{controller.error.message}</p>}
      {controller.attempt && !controller.pending && (
        <>
          <p>
            {controller.error?.outcomeUnknown ? "The outcome remains unconfirmed. " : ""}Retry the
            retained request for {controller.attempt.name} with the same identifier and fields.
          </p>
          <Button onPress={controller.retry}>Retry saved-resource request</Button>
        </>
      )}
      {!!confirmation && <p role="status">{confirmation}</p>}
    </section>
  );
}

export function SavedResources({
  controller,
  onOpen,
  onSearch,
}: {
  controller: SavedResourcesController;
  onOpen: (item: SearchResult) => void;
  onSearch: () => void;
}) {
  const { page } = controller;
  const heading = useRef<HTMLHeadingElement>(null),
    removedControl = useRef<Element | null>(null),
    continuation = useRef(false);
  useEffect(() => {
    if (page.loading || controller.pending) return;
    const removed = removedControl.current && !removedControl.current.isConnected;
    if (
      (removed || continuation.current) &&
      document.activeElement === document.body &&
      !heading.current?.closest("[hidden],[inert]")
    )
      heading.current?.focus();
    if (removed) removedControl.current = null;
    continuation.current = false;
  }, [page, controller.pending]);
  return (
    <section className="workbench-home-section" aria-labelledby="home-saved-title">
      <h2 id="home-saved-title" ref={heading} tabIndex={-1}>
        Saved resources
      </h2>
      <p>
        References saved for your identity in this scope. Open a resource to check its current
        access and state.
      </p>
      <div className="resource-browser__toolbar">
        <Button
          variant="quiet"
          isDisabled={page.loading || controller.blocked}
          onPress={() => void page.load()}
        >
          Refresh saved resources
        </Button>
        <Button variant="quiet" onPress={onSearch}>
          Find resources to save
        </Button>
      </div>
      {page.loading && <p role="status">Loading saved resources…</p>}
      {page.error && (
        <p role="alert" className="workbench-inline-error">
          {page.error.message}
        </p>
      )}
      {!page.loading && !page.error && (
        <>
          <p role="status">
            {page.items.length} saved {page.items.length === 1 ? "resource" : "resources"} loaded.
          </p>
          <DataTable
            label="Saved resources"
            items={page.items}
            getRowId={(item) => item.resourceId}
            getRowLabel={(item) =>
              `${item.resource?.name ?? "Unavailable resource"}, ${searchKindLabels[item.kind]}`
            }
            emptyMessage="No saved resources yet. Use Search to find work and save a reference here."
            columns={[
              {
                id: "name",
                title: "Name",
                isRowHeader: true,
                render: (item) => (
                  <div className="resource-search__identity">
                    <strong>{item.resource?.name ?? "Resource unavailable"}</strong>
                    {!item.resource && <small>{item.resourceId}</small>}
                  </div>
                ),
              },
              { id: "kind", title: "Type", render: (item) => searchKindLabels[item.kind] },
              {
                id: "state",
                title: "State",
                render: (item) =>
                  item.resource && "state" in item.resource ? (
                    <StatusBadge tone={item.resource.state === "failed" ? "critical" : "neutral"}>
                      {item.resource.state === "unknown" ? "Unconfirmed" : item.resource.state}
                    </StatusBadge>
                  ) : item.resource ? (
                    "—"
                  ) : (
                    "Unavailable"
                  ),
              },
              {
                id: "saved",
                title: "Saved",
                render: (item) => (
                  <time dateTime={item.savedAt}>{new Date(item.savedAt).toLocaleString()}</time>
                ),
              },
              {
                id: "actions",
                title: "Actions",
                render: (item) => (
                  <div className="resource-browser__actions">
                    <Button
                      variant="quiet"
                      isDisabled={!item.resource}
                      aria-label={`Open saved ${searchKindLabels[item.kind].toLowerCase()} ${item.resource?.name ?? "unavailable resource"}`}
                      onPress={() => {
                        if (page.canAct() && item.resource) onOpen(item.resource);
                      }}
                    >
                      Open
                    </Button>
                    <Button
                      variant="quiet"
                      isDisabled={controller.blocked}
                      aria-label={`Remove saved ${searchKindLabels[item.kind].toLowerCase()} ${item.resource?.name ?? item.resourceId}`}
                      onPress={() => {
                        if (page.canAct()) {
                          removedControl.current = document.activeElement;
                          controller.change(
                            item.kind,
                            item.resourceId,
                            item.resource?.name ?? "unavailable resource",
                            false,
                          );
                        }
                      }}
                    >
                      Remove
                    </Button>
                  </div>
                ),
              },
            ]}
          />
          {page.nextCursor && (
            <Button
              variant="quiet"
              isDisabled={controller.blocked}
              onPress={() => {
                continuation.current = true;
                void page.load(page.nextCursor);
              }}
            >
              Load more saved resources
            </Button>
          )}
        </>
      )}
    </section>
  );
}
