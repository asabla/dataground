import type { AgentServiceResource } from "./client";

export type ServiceSort = "updated" | "name";

export function selectDirectoryServices(
  services: readonly AgentServiceResource[],
  sort: ServiceSort,
): AgentServiceResource[] {
  return [...services].sort((left, right) => {
    const primary =
      sort === "name"
        ? left.name.localeCompare(right.name)
        : Date.parse(right.metadata.updatedAt) - Date.parse(left.metadata.updatedAt);
    return primary || left.metadata.id.localeCompare(right.metadata.id);
  });
}
