import { Button, StatusBadge } from "@dataground/ui";
import type { ReactNode } from "react";
import type { WorkbenchView } from "./navigation";

export function WorkbenchShell({
  isolationDomainId,
  onDisconnect,
  view,
  onNavigate,
  children,
}: {
  isolationDomainId: string;
  onDisconnect: () => void;
  view: WorkbenchView;
  onNavigate: (view: WorkbenchView) => void;
  children: ReactNode;
}) {
  return (
    <main className="workbench-shell">
      <header className="workbench-topbar">
        <div className="workbench-brand">
          <span aria-hidden="true" className="workbench-brand__mark">
            D
          </span>
          <strong>DataGround</strong>
        </div>
        <section aria-label="Active isolation scope" className="workbench-scope">
          <span>Development scope</span>
          <code>{isolationDomainId}</code>
        </section>
        <div className="workbench-topbar__actions">
          <StatusBadge tone="active">Local development</StatusBadge>
          <Button onPress={onDisconnect} variant="quiet">
            Disconnect
          </Button>
        </div>
      </header>
      <div className="workbench-layout">
        <aside className="workbench-sidebar">
          <p className="workbench-sidebar__label">Workbench</p>
          <nav aria-label="Primary navigation">
            <Button
              aria-current={view !== "interactions" ? "page" : undefined}
              className="workbench-nav-button"
              onPress={() => onNavigate("services")}
              variant="quiet"
            >
              Agent services
            </Button>
            <Button
              aria-current={view === "interactions" ? "page" : undefined}
              className="workbench-nav-button"
              onPress={() => onNavigate("interactions")}
              variant="quiet"
            >
              Interactions
            </Button>
          </nav>
        </aside>
        {children}
      </div>
    </main>
  );
}
