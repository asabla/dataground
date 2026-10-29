import { Button, DataTable, SelectField, TextField } from "@dataground/ui";
import { useRef } from "react";
import type { AgentServiceFailure, AgentServiceResource } from "./client";
import { type ServiceSort, selectDirectoryServices } from "./directory";

interface ServiceDirectoryProps {
  services: AgentServiceResource[];
  loading: boolean;
  loadingMore: boolean;
  error?: AgentServiceFailure;
  hasMore: boolean;
  query: string;
  sort: ServiceSort;
  onQueryChange: (value: string) => void;
  onSortChange: (value: ServiceSort) => void;
  onRefresh: () => void;
  onLoadMore: () => void;
  onCreate: () => void;
  onOpen: (service: AgentServiceResource) => void;
  onHistory: (service: AgentServiceResource) => void;
}

export function ServiceDirectory({
  services,
  loading,
  loadingMore,
  error,
  hasMore,
  query,
  sort,
  onQueryChange,
  onSortChange,
  onRefresh,
  onLoadMore,
  onCreate,
  onOpen,
  onHistory,
}: ServiceDirectoryProps) {
  const visible = selectDirectoryServices(services, query, sort);
  const unavailable = loading || error !== undefined;
  const directory = useRef<HTMLElement>(null);
  const focusSearch = () => directory.current?.querySelector<HTMLInputElement>("input")?.focus();
  return (
    <section aria-label="Service directory" className="service-directory" ref={directory}>
      <div className="service-directory__toolbar">
        <TextField
          label="Search loaded services"
          description="Search by name, description, or service ID."
          type="search"
          maxLength={128}
          value={query}
          onChange={onQueryChange}
        />
        <SelectField
          label="Sort services"
          value={sort}
          options={[
            { label: "Recently updated", value: "updated" },
            { label: "Name", value: "name" },
          ]}
          onChange={(value) => {
            if (value === "updated" || value === "name") onSortChange(value);
          }}
        />
        <Button
          isDisabled={loading || loadingMore}
          onPress={() => {
            focusSearch();
            onRefresh();
          }}
          variant="quiet"
        >
          {loading ? "Refreshing services…" : "Refresh services"}
        </Button>
      </div>

      <p className="service-directory__count" role="status">
        {loading
          ? "Loading services…"
          : error
            ? "Service list could not be updated. Retry before opening a service."
            : `${visible.length} of ${services.length} loaded services shown.`}
        {hasMore && " More services are available. Load more to include them in your search."}
      </p>

      {error && (
        <div className="workbench-inline-error" role="alert">
          <p>{error.message}</p>
          <Button
            isDisabled={loading || loadingMore}
            onPress={() => {
              focusSearch();
              onRefresh();
            }}
            variant="quiet"
          >
            Retry service discovery
          </Button>
        </div>
      )}

      {visible.length > 0 ? (
        <DataTable
          label="Loaded agent services"
          items={visible}
          getRowId={(service) => service.metadata.id}
          getRowLabel={(service) => service.name}
          isLoading={loading}
          columns={[
            {
              id: "name",
              title: "Service",
              isRowHeader: true,
              render: (service) => (
                <div className="service-directory__identity">
                  <strong>{service.name}</strong>
                  <p>{service.description || "No description"}</p>
                </div>
              ),
            },
            {
              id: "updated",
              title: "Updated",
              render: (service) => (
                <time dateTime={service.metadata.updatedAt}>
                  {new Date(service.metadata.updatedAt).toLocaleString()}
                </time>
              ),
            },
            {
              id: "actions",
              title: "Actions",
              render: (service) => (
                <div className="service-directory__actions">
                  <Button
                    aria-label={`Open service ${service.name}`}
                    isDisabled={unavailable}
                    onPress={() => onOpen(service)}
                    variant="quiet"
                  >
                    Open service
                  </Button>
                  <Button
                    aria-label={`View interactions for ${service.name}`}
                    isDisabled={unavailable}
                    onPress={() => onHistory(service)}
                    variant="quiet"
                  >
                    View interactions
                  </Button>
                </div>
              ),
            },
          ]}
        />
      ) : !loading && !error ? (
        <div className="workbench-empty">
          <h2>
            {services.length > 0 || query.trim()
              ? "No matching loaded services"
              : "Create your first agent service"}
          </h2>
          <p>
            {services.length > 0 || query.trim()
              ? "Change your search or load more services if available."
              : "Give your agent a name, configure a revision, then publish it and choose a route to run it."}
          </p>
          {query.trim() ? (
            <Button
              onPress={() => {
                focusSearch();
                onQueryChange("");
              }}
              variant="quiet"
            >
              Clear search
            </Button>
          ) : services.length === 0 ? (
            <Button onPress={onCreate}>Create agent service</Button>
          ) : null}
        </div>
      ) : null}

      {hasMore && (
        <div className="service-directory__more">
          <Button
            isDisabled={loading || loadingMore || !!error}
            onPress={() => {
              focusSearch();
              onLoadMore();
            }}
            variant="quiet"
          >
            {loadingMore ? "Loading more services…" : "Load more services"}
          </Button>
        </div>
      )}
    </section>
  );
}
