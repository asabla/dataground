import assert from "node:assert/strict";
import { it } from "vitest";
import { parseRoute, routeHash, type WorkbenchRoute } from "./navigation";

const isolationDomainId = "iso_00000000000000000001";
const serviceId = "svc_00000000000000000001";
it("round-trips scoped directory, create, service and run-list locations", () => {
  for (const route of [
    { isolationDomainId, view: "services" },
    { isolationDomainId, view: "artifacts" },
    { isolationDomainId, view: "home" },
    { isolationDomainId, view: "workspaces" },
    { isolationDomainId, view: "workspaces", workspaceId: "wsp_00000000000000000001" },
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

it("round-trips artifact inspection in its run view and rejects content-bearing artifact links", () => {
  const artifactId = "art_00000000000000000001";
  for (const runTab of [undefined, "requests", "output", "files", "audit"] as const) {
    const route: WorkbenchRoute = {
      isolationDomainId,
      serviceId,
      invocationId,
      artifactId,
      view: "interactions",
      ...(runTab ? { runTab } : {}),
    };
    assert.deepEqual(parseRoute(routeHash(route), isolationDomainId), route);
  }
  const valid = routeHash({
    isolationDomainId,
    serviceId,
    invocationId,
    artifactId,
    view: "interactions",
  });
  for (const bad of [
    valid.replace(artifactId, ""),
    valid.replace(artifactId, "%61rt_00000000000000000001"),
    valid.replace("/artifacts/", "/unknown/"),
    `${valid}/content`,
    `${valid}?token=secret`,
    `${valid}/`,
  ])
    assert.equal(parseRoute(bad, isolationDomainId), undefined);
  assert.throws(() =>
    routeHash({ isolationDomainId, serviceId, view: "interactions", artifactId }),
  );
  assert.throws(() =>
    routeHash({
      isolationDomainId,
      serviceId,
      invocationId,
      view: "interactions",
      artifactId: "../secret",
    }),
  );
});

const revisionId = "rev_00000000000000000001";
it("round-trips revision context across service views and rejects mixed resource locations", () => {
  for (const serviceTab of [undefined, "overview", "versions", "deployments"] as const) {
    const route: WorkbenchRoute = {
      isolationDomainId,
      serviceId,
      revisionId,
      view: "workflow",
      ...(serviceTab ? { serviceTab } : {}),
    };
    assert.deepEqual(parseRoute(routeHash(route), isolationDomainId), route);
  }
  const valid = routeHash({ isolationDomainId, serviceId, revisionId, view: "workflow" });
  for (const bad of [
    `${valid}/content`,
    `${valid}?prompt=secret`,
    valid.replace(revisionId, "../other"),
    valid.replace(revisionId, "%72ev_00000000000000000001"),
    valid.replace("/build/", "/runs/"),
  ])
    assert.equal(parseRoute(bad, isolationDomainId), undefined);
  assert.throws(() => routeHash({ isolationDomainId, revisionId, view: "workflow" }));
  assert.throws(() =>
    routeHash({ isolationDomainId, serviceId, revisionId, invocationId, view: "interactions" }),
  );
});

it("rejects service context and content in global artifact locations", () => {
  assert.throws(() => routeHash({ isolationDomainId, serviceId, view: "artifacts" }));
  assert.equal(
    parseRoute(`#/scopes/${isolationDomainId}/artifacts?content=private`, isolationDomainId),
    undefined,
  );
  assert.equal(
    parseRoute(`#/scopes/${isolationDomainId}/artifacts/extra`, isolationDomainId),
    undefined,
  );
});

it("keeps Home scoped and rejects resource or content context", () => {
  const root = `#/scopes/${isolationDomainId}/home`;
  for (const hash of [
    `${root}/extra`,
    `${root}?prompt=private`,
    root.replace(isolationDomainId, "iso_00000000000000000002"),
  ])
    assert.equal(parseRoute(hash, isolationDomainId), undefined);
  assert.throws(() => routeHash({ isolationDomainId, view: "home", serviceId }));
  assert.throws(() => routeHash({ isolationDomainId, view: "home", runTab: "output" }));
});

it("rejects workspace paths with unrelated resource or content state", () => {
  const workspaceId = "wsp_00000000000000000001";
  for (const view of ["services", "home", "workflow", "artifacts", "interactions"] as const)
    assert.throws(() => routeHash({ isolationDomainId, view, workspaceId }));
  assert.throws(() => routeHash({ isolationDomainId, view: "workspaces", workspaceId, serviceId }));
  for (const suffix of ["/extra", "?prompt=private", "/../other"])
    assert.equal(
      parseRoute(
        routeHash({ isolationDomainId, view: "workspaces", workspaceId }) + suffix,
        isolationDomainId,
      ),
      undefined,
    );
});
