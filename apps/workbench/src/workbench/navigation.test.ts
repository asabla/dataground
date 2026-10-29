import assert from "node:assert/strict";
import { it } from "vitest";
import { parseRoute, routeHash, type WorkbenchRoute } from "./navigation";

const isolationDomainId = "iso_00000000000000000001";
const serviceId = "svc_00000000000000000001";
it("round-trips scoped directory, create, service and run-list locations", () => {
  for (const route of [
    { isolationDomainId, view: "services" },
    { isolationDomainId, view: "workflow" },
    { isolationDomainId, view: "interactions" },
    { isolationDomainId, serviceId, view: "workflow" },
    { isolationDomainId, serviceId, view: "workflow", serviceTab: "overview" },
    { isolationDomainId, serviceId, view: "workflow", serviceTab: "versions" },
    { isolationDomainId, serviceId, view: "workflow", serviceTab: "deployments" },
    { isolationDomainId, serviceId, view: "interactions" },
  ] satisfies WorkbenchRoute[]) {
    assert.deepEqual(parseRoute(routeHash(route), isolationDomainId), route);
  }
});
it("rejects cross-scope, encoded, unknown and content-bearing locations", () => {
  const valid = routeHash({ isolationDomainId, serviceId, view: "workflow" });
  for (const hash of [
    valid.replace(isolationDomainId, "iso_00000000000000000002"),
    valid.replace(serviceId, "../other"),
    valid.replace(serviceId, "%73vc_00000000000000000001"),
    valid.replace("/build", "/unknown"),
    `${valid}?prompt=secret`,
    `${valid}/extra`,
    "#/catalog",
    "#/scopes/bad/agents",
  ])
    assert.equal(parseRoute(hash, isolationDomainId), undefined);
  assert.equal(parseRoute(valid, "bad"), undefined);
  assert.throws(() => routeHash({ isolationDomainId, serviceId: "../other", view: "workflow" }));
});

const invocationId = "inv_00000000000000000001";
it("round-trips exact run locations and closed inspection views", () => {
  for (const runTab of [undefined, "requests", "output", "files", "audit"] as const) {
    const route: WorkbenchRoute = {
      isolationDomainId,
      serviceId,
      view: "interactions",
      invocationId,
      ...(runTab ? { runTab } : {}),
    };
    assert.deepEqual(parseRoute(routeHash(route), isolationDomainId), route);
  }
  const valid = routeHash({ isolationDomainId, serviceId, view: "interactions", invocationId });
  for (const bad of [
    `${valid}/extra`,
    `${valid}?result=secret`,
    valid.replace("/activity", "/unknown"),
    valid.replace(invocationId, "%69nv_00000000000000000001"),
  ]) {
    assert.equal(parseRoute(bad, isolationDomainId), undefined);
  }
  assert.throws(() => routeHash({ isolationDomainId, view: "interactions", invocationId }));
  assert.throws(() => routeHash({ isolationDomainId, serviceId, view: "workflow", invocationId }));
});
