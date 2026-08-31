import type { ServiceRevisionHistoryResource, ServiceRevisionResource } from "./client";
import type { PublishedServiceRevisionResource } from "./publicationClient";

export interface ServiceRevisionResumeSelection {
  publishedRevision?: PublishedServiceRevisionResource;
  revision?: ServiceRevisionResource;
}

function copySchema(
  schema: Record<string, unknown> | undefined,
): Record<string, unknown> | undefined {
  return schema === undefined ? undefined : JSON.parse(JSON.stringify(schema));
}

export function resumeServiceRevision(
  discovered: ServiceRevisionHistoryResource,
): ServiceRevisionResumeSelection {
  const inputSchema = copySchema(discovered.inputSchema);
  const outputSchema = copySchema(discovered.outputSchema);
  const definition = {
    ...(inputSchema === undefined ? undefined : { inputSchema }),
    ...(outputSchema === undefined ? undefined : { outputSchema }),
    requiredCapabilities: [...discovered.requiredCapabilities],
    revisionNumber: discovered.revisionNumber,
    runtimeProfile: discovered.runtimeProfile,
    serviceId: discovered.serviceId,
  };
  if (discovered.state === "draft") {
    return {
      revision: {
        ...definition,
        metadata: { ...discovered.metadata },
        state: "draft",
      },
    };
  }
  if (
    discovered.state !== "published" ||
    discovered.publishedAt === undefined ||
    discovered.metadata.version < 2
  ) {
    return {};
  }

  // Published definitions are immutable. This prior-version scope link is used only
  // to preserve the established service → revision → publication validation chain;
  // the published selection skips the publication command stage entirely.
  const revision: ServiceRevisionResource = {
    ...definition,
    metadata: {
      ...discovered.metadata,
      updatedAt: discovered.metadata.createdAt,
      version: discovered.metadata.version - 1,
    },
    state: "draft",
  };
  return {
    publishedRevision: {
      ...definition,
      metadata: { ...discovered.metadata },
      publishedAt: discovered.publishedAt,
      state: "published",
    },
    revision,
  };
}
