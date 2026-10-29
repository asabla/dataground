import { compareTimestamps } from "../contracts/timestamps";
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
        : compareTimestamps(right.metadata.updatedAt, left.metadata.updatedAt);
    return primary || left.metadata.id.localeCompare(right.metadata.id);
  });
}
