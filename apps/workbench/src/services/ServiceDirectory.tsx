import { Button, SelectField, TextField } from "@dataground/ui";
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
        <ul
          className="service-directory__list"
          aria-label="Loaded agent services"
          aria-busy={loading}
        >
          {visible.map((service) => (
            <li key={service.metadata.id}>
              <div className="service-directory__identity">
                <h2>{service.name}</h2>
                <p>{service.description || "No description"}</p>
                <code>{service.metadata.id}</code>
              </div>
              <div className="service-directory__updated">
                <span>Updated</span>
                <time dateTime={service.metadata.updatedAt}>
                  {new Date(service.metadata.updatedAt).toLocaleString()}
                </time>
              </div>
              <div className="service-directory__actions">
                <Button
                  aria-label={`Open service ${service.name}`}
                  isDisabled={unavailable}
                  onPress={() => onOpen(service)}
                  variant="secondary"
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
            </li>
          ))}
        </ul>
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
