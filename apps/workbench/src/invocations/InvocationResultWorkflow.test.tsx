import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { InvocationResultWorkflow } from "./InvocationResultWorkflow";

it("does not request result content on initial rendering", () => {
  let reads = 0;
  const client = {
    GET: async () => {
      reads++;
    },
  } as unknown as DataGroundClient;
  const markup = renderToStaticMarkup(
    <InvocationResultWorkflow
      client={client}
      reference={{
        invocationId: "inv_00000000000000000001",
        isolationDomainId: "iso_00000000000000000001",
        serviceId: "svc_00000000000000000001",
        revisionId: "rev_00000000000000000001",
      }}
    />,
  );
  assert.equal(reads, 0);
  assert.match(markup, /Show result/);
  assert.doesNotMatch(markup, /<pre/);
});
