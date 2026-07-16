export function App() {
  return (
    <main className="shell">
      <header className="masthead">
        <p className="eyebrow">Self-hosted data and agent platform</p>
        <h1>DataGround</h1>
      </header>

      <section className="status-panel" aria-labelledby="bootstrap-status">
        <div>
          <p className="status-label">
            <span className="status-indicator" aria-hidden="true" />
            Bootstrap status
          </p>
          <h2 id="bootstrap-status">Project foundation ready</h2>
        </div>
        <p className="status-detail">
          The workbench shell is running. Product resources remain unavailable until their versioned
          contracts and authorization behavior are implemented.
        </p>
      </section>
    </main>
  );
}
