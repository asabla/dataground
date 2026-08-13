import createClient from "openapi-fetch";

import type { paths } from "./openapi.gen";

/**
 * Creates a typed DataGround client without embedding a production endpoint.
 * Authentication and isolation authorization remain the caller's responsibility.
 */
export function createDataGroundClient(baseUrl: string) {
  return createClient<paths>({ baseUrl });
}

export type DataGroundClient = ReturnType<typeof createDataGroundClient>;
