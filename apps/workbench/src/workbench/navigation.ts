export type WorkbenchView = "interactions" | "services" | "workflow";
export type ServiceTab = "overview" | "build" | "versions" | "deployments" | "runs";
export type WorkbenchPresentation = "workbench" | "classic";

export function isServiceTab(value: string): value is ServiceTab {
  return ["overview", "build", "versions", "deployments", "runs"].includes(value);
}

export interface WorkbenchRoute {
  isolationDomainId: string;
  view: WorkbenchView;
  serviceId?: string;
  serviceTab?: Exclude<ServiceTab, "build" | "runs">;
}

const scopePattern = /^iso_[0-9a-z]{20,32}$/u;
const servicePattern = /^svc_[0-9a-z]{20,32}$/u;

// Only resource identities and closed view names belong in navigation. Forms,
// command attempts, filters and result contents stay in the connected session.
export function routeHash(route: WorkbenchRoute): string {
  if (
    !scopePattern.test(route.isolationDomainId) ||
    (route.serviceId !== undefined && !servicePattern.test(route.serviceId))
  ) {
    throw new Error("Invalid workbench route");
  }
  const root = `#/scopes/${route.isolationDomainId}/agents`;
  if (route.view === "services") return root;
  if (route.serviceId) {
    return `${root}/${route.serviceId}/${route.view === "interactions" ? "runs" : (route.serviceTab ?? "build")}`;
  }
  return `${root}/${route.view === "workflow" ? "new" : "runs"}`;
}

export function parseRoute(hash: string, isolationDomainId: string): WorkbenchRoute | undefined {
  if (!scopePattern.test(isolationDomainId)) return undefined;
  if (hash === "" || hash === "#") return { isolationDomainId, view: "services" };
  const root = `#/scopes/${isolationDomainId}/agents`;
  if (hash === root) return { isolationDomainId, view: "services" };
  if (hash === `${root}/new`) return { isolationDomainId, view: "workflow" };
  if (hash === `${root}/runs`) return { isolationDomainId, view: "interactions" };
  if (!hash.startsWith(`${root}/`)) return undefined;
  const [serviceId, tab, extra] = hash.slice(root.length + 1).split("/");
  if (
    !serviceId ||
    !servicePattern.test(serviceId) ||
    extra !== undefined ||
    !tab ||
    !isServiceTab(tab)
  )
    return undefined;
  return {
    isolationDomainId,
    serviceId,
    view: tab === "runs" ? "interactions" : "workflow",
    ...(tab === "build" || tab === "runs" ? {} : { serviceTab: tab }),
  };
}
