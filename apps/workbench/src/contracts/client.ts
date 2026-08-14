import createClient from "openapi-fetch";

import type { paths } from "./openapi.gen";

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
  return createClient<paths>({
    baseUrl,
    fetch,
    headers: bearerToken === undefined ? undefined : { Authorization: `Bearer ${bearerToken}` },
  });
}

export type DataGroundClient = ReturnType<typeof createDataGroundClient>;
