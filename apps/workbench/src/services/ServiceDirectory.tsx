import { Button, DataTable, SelectField, TextField } from "@dataground/ui";
import { useRef } from "react";
import type { AgentServiceFailure, AgentServiceResource } from "./client";
import { type ServiceSort, selectDirectoryServices } from "./directory";

interface ServiceDirectoryProps {
  services: AgentServiceResource[];
  loading: boolean;
  error?: AgentServiceFailure;
  hasMore: boolean;
  query: string;
  appliedQuery: string;
  onSearch: (value: string) => void;
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
  error,
  hasMore,
  query,
  appliedQuery,
  onSearch,
  sort,
  onQueryChange,
  onSortChange,
  onRefresh,
  onLoadMore,
  onCreate,
  onOpen,
  onHistory,
}: ServiceDirectoryProps) {
  const visible = selectDirectoryServices(services, sort);
  const unavailable = loading || error !== undefined;
  const directory = useRef<HTMLElement>(null);
  const focusSearch = () => directory.current?.querySelector<HTMLInputElement>("input")?.focus();
  return (
    <section aria-label="Service directory" className="service-directory" ref={directory}>
      <form
        className="service-directory__toolbar"
        onSubmit={(event) => {
          event.preventDefault();
          onSearch(query.trim());
        }}
      >
        <TextField
          label="Search services"
          description="Search by name, description, or service ID."
          type="search"
          maxLength={128}
          value={query}
          onChange={onQueryChange}
        />
        <Button type="submit">Search</Button>
        <SelectField
          label="Sort loaded results"
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
          isDisabled={loading}
          onPress={() => {
            focusSearch();
            onRefresh();
          }}
          variant="quiet"
        >
          {loading ? "Refreshing services…" : "Refresh services"}
        </Button>
      </form>

      <p className="service-directory__count" role="status">
        {loading
          ? "Loading services…"
          : error
            ? "Service list could not be updated. Retry before opening a service."
            : `${visible.length} services loaded${appliedQuery ? ` for “${appliedQuery}”` : ""}.`}
        {hasMore && " More matching services are available."}
      </p>

      {error && (
        <div className="workbench-inline-error" role="alert">
          <p>{error.message}</p>
          <Button
            isDisabled={loading}
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
          <h2>{appliedQuery ? "No matching services" : "Create your first agent service"}</h2>
          <p>
            {appliedQuery
              ? "Change your search to find other services."
              : "Give your agent a name, configure a revision, then publish it and choose a route to run it."}
          </p>
          {appliedQuery ? (
            <Button
              onPress={() => {
                focusSearch();
                onQueryChange("");
                onSearch("");
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
            isDisabled={loading || !!error}
            onPress={() => {
              focusSearch();
              onLoadMore();
            }}
            variant="quiet"
          >
            {loading ? "Loading services…" : "Load more services"}
          </Button>
        </div>
      )}
    </section>
  );
}
