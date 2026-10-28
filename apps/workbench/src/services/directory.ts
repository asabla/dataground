import type { AgentServiceResource } from "./client";

export type ServiceSort = "updated" | "name";

// Search is confined to validated, loaded resources. It must not imply that
// later server pages were searched or that service metadata proves runtime state.
export function selectDirectoryServices(
  services: readonly AgentServiceResource[],
  query: string,
  sort: ServiceSort,
): AgentServiceResource[] {
  const term = query.trim().toLocaleLowerCase();
  return services
    .filter((service) =>
      [service.name, service.description ?? "", service.metadata.id].some((value) =>
        value.toLocaleLowerCase().includes(term),
      ),
    )
    .sort((left, right) => {
      const primary =
        sort === "name"
          ? left.name.localeCompare(right.name)
          : Date.parse(right.metadata.updatedAt) - Date.parse(left.metadata.updatedAt);
      return primary || left.metadata.id.localeCompare(right.metadata.id);
    });
}
