import createClient from "openapi-fetch";

import type { operations, paths } from "./openapi.gen";

export interface DataGroundClientOptions {
  bearerToken?: string;
  fetch?: typeof globalThis.fetch;
}

/**
 * Creates a typed DataGround client without embedding a production endpoint.
 * Authentication and isolation authorization remain the caller's responsibility.
 */
export function createDataGroundClient(
  baseUrl: string,
  { bearerToken, fetch }: DataGroundClientOptions = {},
) {
  const client = createClient<paths>({
    baseUrl,
    fetch,
    headers: bearerToken === undefined ? undefined : { Authorization: `Bearer ${bearerToken}` },
  });
  return Object.assign(client, {
    // Return the raw response so both success and error bodies can be bounded
    // before parsing. openapi-fetch eagerly buffers unsuccessful responses.
    fetchArtifactContent(
      reference: operations["readInvocationArtifactContent"]["parameters"]["path"],
      signal: AbortSignal,
    ): Promise<Response> {
      const path = `/v1/isolation-domains/${encodeURIComponent(reference.isolationDomainId)}/invocations/${encodeURIComponent(reference.invocationId)}/artifacts/${encodeURIComponent(reference.artifactId)}/content`;
      return (fetch ?? globalThis.fetch)(`${baseUrl.replace(/\/$/u, "")}${path}`, {
        method: "GET",
        headers: bearerToken === undefined ? undefined : { Authorization: `Bearer ${bearerToken}` },
        signal,
        cache: "no-store",
        credentials: "omit",
        redirect: "error",
      });
    },
  });
}

export type DataGroundClient = ReturnType<typeof createDataGroundClient>;
