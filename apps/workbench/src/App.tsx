import { Button, StatusBadge } from "@dataground/ui";
import { useState } from "react";

export function App() {
  const [detailsVisible, setDetailsVisible] = useState(false);

  return (
    <main className="shell">
      <header className="masthead">
        <p className="eyebrow">Self-hosted data and agent platform</p>
        <h1>DataGround</h1>
      </header>

      <section className="status-panel" aria-labelledby="bootstrap-status">
        <div>
          <StatusBadge tone="active">Bootstrap status</StatusBadge>
          <h2 id="bootstrap-status">Project foundation ready</h2>
        </div>
        <div className="status-copy">
          <p className="status-detail">
            The workbench shell is running. Product resources remain unavailable until their
            versioned contracts and authorization behavior are implemented.
          </p>
          <Button
            aria-controls="implementation-contract"
            aria-expanded={detailsVisible}
            onPress={() => setDetailsVisible((visible) => !visible)}
            variant="quiet"
          >
            {detailsVisible ? "Hide implementation contract" : "Show implementation contract"}
          </Button>
        </div>
      </section>

      {detailsVisible && (
        <section className="implementation-contract" id="implementation-contract">
          <h2>Workbench implementation contract</h2>
          <p>
            Shared tokens and accessible primitives are active. Product modules remain responsible
            for authorization, data loading, commands, and durable resource state.
          </p>
        </section>
      )}
    </main>
  );
}
