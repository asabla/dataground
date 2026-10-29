import { useCallback, useEffect, useState } from "react";
import { parseRoute, routeHash, type WorkbenchRoute } from "./navigation";

export function useWorkbenchNavigation(isolationDomainId: string) {
  const read = useCallback(
    () => parseRoute(typeof window === "undefined" ? "" : window.location.hash, isolationDomainId),
    [isolationDomainId],
  );
  const [route, setRoute] = useState(read);
  useEffect(() => {
    const changed = () => {
      const next = read();
      setRoute((current) =>
        current?.isolationDomainId === next?.isolationDomainId &&
        current?.serviceId === next?.serviceId &&
        current?.serviceTab === next?.serviceTab &&
        current?.view === next?.view
          ? current
          : next,
      );
    };
    window.addEventListener("hashchange", changed);
    window.addEventListener("popstate", changed);
    changed();
    return () => {
      window.removeEventListener("hashchange", changed);
      window.removeEventListener("popstate", changed);
    };
  }, [read]);
  const navigate = useCallback(
    (next: WorkbenchRoute) => {
      if (next.isolationDomainId !== isolationDomainId) return;
      const hash = routeHash(next);
      if (window.location.hash !== hash) window.history.pushState(null, "", hash);
      setRoute(parseRoute(hash, isolationDomainId));
    },
    [isolationDomainId],
  );
  return { route, navigate };
}
