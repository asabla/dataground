import { ActionMenu, Button, SelectField, StatusBadge } from "@dataground/ui";
import { type ReactNode, useState } from "react";
import type { WorkbenchView } from "./navigation";

export function WorkbenchShell({
  isolationDomainId,
  onDisconnect,
  view,
  onNavigate,
  onCreate,
  children,
}: {
  isolationDomainId: string;
  onDisconnect: () => void;
  view: WorkbenchView;
  onNavigate: (view: WorkbenchView) => void;
  onCreate: (kind: "service" | "workspace") => void;
  children: ReactNode;
}) {
  const [theme, setTheme] = useState<"system" | "light" | "night">("system");
  return (
    <main className="workbench-shell" data-theme={theme === "system" ? undefined : theme}>
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
          <ActionMenu
            label="Create resource"
            triggerLabel="New"
            items={[
              { id: "service", label: "Ungrouped agent service" },
              { id: "workspace", label: "Workspace" },
            ]}
            onAction={(kind) => {
              if (kind === "service" || kind === "workspace") onCreate(kind);
            }}
          />
          <SelectField
            label="Theme"
            value={theme}
            options={[
              { value: "system", label: "System" },
              { value: "light", label: "Light" },
              { value: "night", label: "Dark" },
            ]}
            onChange={(value) => {
              if (value === "system" || value === "light" || value === "night") setTheme(value);
            }}
          />
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
              aria-current={view === "home" ? "page" : undefined}
              className="workbench-nav-button"
              onPress={() => onNavigate("home")}
              variant="quiet"
            >
              Home
            </Button>
            <Button
              aria-current={view === "workspaces" ? "page" : undefined}
              className="workbench-nav-button"
              onPress={() => onNavigate("workspaces")}
              variant="quiet"
            >
              Workspace
            </Button>
            <Button
              aria-current={view === "services" || view === "workflow" ? "page" : undefined}
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
              Runs
            </Button>
            <Button
              aria-current={view === "artifacts" ? "page" : undefined}
              className="workbench-nav-button"
              onPress={() => onNavigate("artifacts")}
              variant="quiet"
            >
              Artifacts
            </Button>
          </nav>
        </aside>
        {children}
      </div>
    </main>
  );
}
