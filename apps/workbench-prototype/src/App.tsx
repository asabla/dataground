import { type ReactNode, useEffect, useReducer, useRef, useState } from "react";
import { columns, notebookCode, notes, sectionPaths, sections } from "./data";
import { type Action, initialModel, parseRoute, reducer, type Scenario } from "./model";
import {
  Admin,
  Agents,
  Artifacts,
  Catalog,
  Home,
  Jobs,
  ResourceTable,
  Runs,
  SQL,
  Workspace,
} from "./pages";
import type { Inspector, PageProps } from "./types";
import {
  Badge,
  Button,
  Details,
  Empty,
  Field,
  Filter,
  Header,
  Icon,
  Link,
  Notice,
  Panel,
  Table,
} from "./ui";

const go = (path: string) => {
  window.location.hash = path;
};
export function App() {
  const [route, setRoute] = useState(() => parseRoute(window.location.hash));
  const [model, dispatch] = useReducer(reducer, initialModel);
  const [theme, setTheme] = useState("light");
  const [workspace, setWorkspace] = useState("retail");
  const [scenario, setScenario] = useState<Scenario>("normal");
  const [panel, setPanel] = useState<Inspector>(null);
  const [searches, setSearches] = useState<Record<string, string>>({});
  const [globalSearch, setGlobalSearch] = useState("");
  const [newName, setNewName] = useState("Forecast assistant");
  const [newPurpose, setNewPurpose] = useState(
    "Explain regional revenue trends using reviewed commerce data.",
  );
  const [prompt, setPrompt] = useState(
    "Summarize October revenue by region and prepare a workspace report.",
  );
  const [feedback, setFeedback] = useState("");
  const main = useRef<HTMLElement>(null);
  useEffect(() => {
    const update = () => {
      setRoute(parseRoute(window.location.hash));
      setScenario("normal");
      setPanel(null);
      setFeedback("");
    };
    window.addEventListener("hashchange", update);
    return () => window.removeEventListener("hashchange", update);
  }, []);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, [theme]);
  useEffect(() => {
    if (route.area) main.current?.querySelector<HTMLElement>("h1")?.focus();
  }, [route]);
  useEffect(() => {
    if (model.queryStatus !== "running") return;
    const timer = setTimeout(() => dispatch({ type: "query-complete" }), 900);
    return () => clearTimeout(timer);
  }, [model.queryStatus]);
  useEffect(() => {
    if (model.notebookStatus !== "running") return;
    const timer = setTimeout(() => dispatch({ type: "notebook-complete" }), 700);
    return () => clearTimeout(timer);
  }, [model.notebookStatus]);
  const props: PageProps = {
    route,
    model,
    dispatch,
    scenario,
    inspect: setPanel,
    query: searches[route.area] || "",
    setQuery: (value) => setSearches((current) => ({ ...current, [route.area]: value })),
  };
  let page: ReactNode;
  if (workspace === "marketing")
    page = (
      <>
        <Header title="Marketing sandbox" description="A separate, empty sample workspace." />
        <Empty title="No work here yet">
          Switch to Retail operations to explore the connected sample journeys.
        </Empty>
      </>
    );
  else if (scenario === "loading")
    page = (
      <>
        <Header
          title="Loading resources"
          description="The current scope stays visible while data is requested."
        />
        <div className="content-pad" role="status" aria-label="Loading sample resources">
          <div className="skeleton" />
          <div className="skeleton" />
          <div className="skeleton" />
          <Button onPress={() => setScenario("normal")}>Finish sample load</Button>
        </div>
      </>
    );
  else if (scenario === "denied")
    page = (
      <>
        <Header title="Access unavailable" />
        <div className="content-pad">
          <Notice title="You do not have access to this resource" tone="warning">
            No resource contents are shown.{" "}
            <Button onPress={() => setPanel("access")}>Inspect access</Button>
          </Notice>
        </div>
      </>
    );
  else if (scenario === "empty")
    page = (
      <>
        <Header title="No matching work" />
        <Empty title="There are no resources in this view">
          Clear the sample state to return to the populated workspace.{" "}
          <Button onPress={() => setScenario("normal")}>Show sample resources</Button>
        </Empty>
      </>
    );
  else if (scenario === "offline")
    page = (
      <>
        <Header title="Connection unavailable" />
        <div className="content-pad">
          <Notice title="The current state cannot be confirmed" tone="warning">
            Drafts remain in memory. Commands are unavailable until this sample connection is
            restored. <Button onPress={() => setScenario("normal")}>Reconnect sample</Button>
          </Notice>
        </div>
      </>
    );
  else
    page =
      route.area === "catalog" ? (
        <Catalog {...props} />
      ) : route.area === "workspace" ? (
        <Workspace {...props} />
      ) : route.area === "sql" ? (
        <SQL {...props} />
      ) : route.area === "agents" ? (
        <Agents {...props} />
      ) : route.area === "jobs" ? (
        <Jobs {...props} />
      ) : route.area === "runs" ? (
        <Runs key={route.id} {...props} />
      ) : route.area === "artifacts" ? (
        <Artifacts key={route.id} {...props} />
      ) : route.area === "admin" ? (
        <Admin {...props} />
      ) : (
        <Home {...props} />
      );
  const panelTitle =
    panel === "guide"
      ? "Design review guide"
      : panel === "search"
        ? "Search this sample workspace"
        : panel === "create"
          ? "Create in Retail operations"
          : panel === "publish"
            ? "Review version publication"
            : panel === "route"
              ? "Assign deployment route"
              : panel === "invoke"
                ? "Run agent"
                : panel === "approval"
                  ? "Review publication approval"
                  : panel === "task"
                    ? "Validate totals"
                    : panel === "policy"
                      ? "Policy summary"
                      : panel === "access"
                        ? "Access explanation"
                        : panel === "runtime" || panel === "sql-runtime"
                          ? "Environment readiness"
                          : panel === "catalog"
                            ? "Source data"
                            : panel === "new-agent"
                              ? "New agent service"
                              : panel === "discard"
                                ? "Notebook version conflict"
                                : panel === "cancel"
                                  ? "Cancel this run?"
                                  : "Technical details";
  const finish = (action: Action, target?: string) => {
    dispatch(action);
    setPanel(null);
    if (target) go(target);
  };
  return (
    <div className="prototype" data-density="standard">
      <button
        type="button"
        className="skip-link"
        onClick={(event) => {
          event.preventDefault();
          main.current?.focus();
        }}
      >
        Skip to content
      </button>
      <div className="review-bar">
        <strong>Design preview</strong>
        <span>Fictional data · Simulated actions · No backend connection</span>
        <label>
          Review state
          <select
            aria-label="Review state"
            value={scenario}
            onChange={(event) => {
              setScenario(event.target.value as Scenario);
              if (event.target.value === "cancelling") dispatch({ type: "cancel" });
            }}
          >
            <option value="normal">Normal</option>
            <option value="loading">Loading</option>
            <option value="empty">Empty</option>
            <option value="denied">Access denied</option>
            <option value="offline">Disconnected</option>
            {route.area === "sql" && <option value="failed">Execution failed</option>}
            {route.area === "workspace" && route.id && (
              <option value="conflict">Save conflict</option>
            )}
            {route.area === "agents" && route.id && (
              <option value="uncertain">Uncertain publication</option>
            )}
            {route.area === "runs" && route.id === "agent-review" && (
              <>
                <option value="expired">Expired request</option>
                <option value="cancelling">Cancelling</option>
              </>
            )}
          </select>
        </label>
        <Button variant="quiet" onPress={() => setPanel("guide")}>
          Review guide
        </Button>
      </div>
      <header className="app-header">
        <Link to="/home" className="brand">
          <span className="brand-mark" aria-hidden="true">
            <span />
            <span />
            <span />
          </span>
          DataGround
        </Link>
        <div className="scope">
          <span>Northstar Retail</span>
          <span className="scope-divider">/</span>
          <select
            aria-label="Workspace"
            value={workspace}
            onChange={(event) => {
              setWorkspace(event.target.value);
              setPanel(null);
              setGlobalSearch("");
              setPrompt("Summarize October revenue by region and prepare a workspace report.");
              setNewName("Forecast assistant");
              setNewPurpose("Explain regional revenue trends using reviewed commerce data.");
              dispatch({ type: "reset" });
              setSearches({});
              setScenario("normal");
              go("/home");
            }}
          >
            <option value="retail">Retail operations</option>
            <option value="marketing">Marketing sandbox</option>
          </select>
        </div>
        <Button className="global-search" variant="quiet" onPress={() => setPanel("search")}>
          <Icon name="search" />
          <span>Search workspace</span>
        </Button>
        <Button
          variant="quiet"
          aria-label={theme === "light" ? "Switch to dark theme" : "Switch to light theme"}
          onPress={() => setTheme(theme === "light" ? "night" : "light")}
        >
          {theme === "light" ? "Dark" : "Light"}
        </Button>
        <span className="avatar" title="Alex Morgan · Fictional review identity">
          AM
        </span>
      </header>
      <div className="app-layout">
        <aside className="sidebar">
          <Button
            variant="primary"
            onPress={() => setPanel("create")}
            isDisabled={workspace !== "retail" || scenario === "offline" || scenario === "loading"}
          >
            <Icon name="plus" /> New
          </Button>
          <nav aria-label="Primary navigation">
            {sections.map((label, i) => {
              const path = sectionPaths[i] as string;
              const area = path.split("/")[1];
              const active = route.area === area || (area === "jobs" && route.area === "runs");
              return (
                <Link
                  key={label}
                  to={path}
                  className={`nav-item ${active ? "active" : ""}`}
                  current={active}
                >
                  <Icon name={area || "home"} />
                  <span>{label}</span>
                </Link>
              );
            })}
          </nav>
          <div className="sidebar-bottom">
            <span className="eyebrow">Design review workspace</span>
            <p>One sample. Connected journeys.</p>
            <Button variant="quiet" onPress={() => setPanel("guide")}>
              What to review
            </Button>
          </div>
        </aside>
        <main id="main-content" tabIndex={-1} ref={main}>
          {feedback && <Notice title={feedback} tone="success" />}
          {page}
        </main>
      </div>
      {panel && (
        <Panel
          title={panelTitle}
          onClose={() => setPanel(null)}
          drawer={[
            "guide",
            "task",
            "access",
            "runtime",
            "sql-runtime",
            "metadata",
            "catalog",
            "approval",
          ].includes(panel)}
        >
          {panel === "guide" ? (
            <>
              <Badge>Prototype only</Badge>
              <h3>This screen</h3>
              <ol className="guide-list">
                {(notes[route.area] || notes.home)?.map((note) => (
                  <li key={note}>{note}</li>
                ))}
              </ol>
              <h3>Connected journeys</h3>
              <div className="guide-links">
                <Link to="/catalog/orders">1. Discover → query → save</Link>
                <Link to="/workspace/revenue-notebook">2. Notebook → data → results</Link>
                <Link to="/agents/revenue?tab=build">3. Build → publish → route → run</Link>
                <Link to="/runs/daily-refresh">4. Failure → task → recovery</Link>
                <Link to="/artifacts/revenue-summary">5. Output → provenance → source</Link>
                <Link to="/admin">6. Access and runtime inspection</Link>
              </div>
              <p>
                Use Review state above the workbench to inspect failure and recovery views. Dark and
                light themes are available in the header.
              </p>
              <Notice title="Review gate">
                This prototype is not the production workbench. No live data, execution,
                permissions, or retention guarantees are established here.
              </Notice>
              <Button
                onPress={() => {
                  dispatch({ type: "reset" });
                  setScenario("normal");
                  setPanel(null);
                  go("/home");
                }}
              >
                Reset sample journey
              </Button>
            </>
          ) : panel === "search" ? (
            <>
              <Filter
                value={globalSearch}
                onChange={setGlobalSearch}
                placeholder="Search sample resources"
              />
              <ResourceTable query={globalSearch} />
              <p className="muted">Search covers only the named fixtures in Retail operations.</p>
            </>
          ) : panel === "create" ? (
            <div className="create-options">
              <Link to="/sql/revenue">
                <Icon name="sql" /> SQL query <small>Explore the regional revenue sample</small>
              </Link>
              <Link to="/workspace/revenue-notebook">
                <Icon name="notebook" /> Notebook{" "}
                <small>Open the connected analysis notebook</small>
              </Link>
              <Button variant="quiet" onPress={() => setPanel("new-agent")}>
                <Icon name="agents" /> Agent service
              </Button>
              <p className="muted">
                Query and notebook actions open editable sample documents. They do not create
                production resources.
              </p>
            </div>
          ) : panel === "new-agent" ? (
            <>
              <Field label="Service name" value={newName} onChange={setNewName} />
              <Field label="Purpose" value={newPurpose} onChange={setNewPurpose} multiline />
              <p>This creates a new local draft in the prototype's editable service slot.</p>
              <Button
                variant="primary"
                isDisabled={!newName.trim() || !newPurpose.trim()}
                onPress={() =>
                  finish(
                    { type: "agent-new", name: newName, purpose: newPurpose },
                    "/agents/revenue?tab=build",
                  )
                }
              >
                Create sample draft
              </Button>
            </>
          ) : panel === "publish" ? (
            <>
              <p>
                Publish this exact definition as an immutable version. Publication does not assign a
                route or start a run.
              </p>
              <Details
                values={[
                  ["Workspace", "Retail operations"],
                  ["Service", model.agentName],
                  ["Version", "2"],
                  ["Runtime", "Governed analyst"],
                ]}
              />
              <h3>Instructions</h3>
              <p>{model.purpose}</p>
              <Notice title="Input and output">
                Required text prompt → revenue summary and artifact reference.
              </Notice>
              <Button
                variant="primary"
                onPress={() => finish({ type: "publish", uncertain: scenario === "uncertain" })}
              >
                Publish version 2
              </Button>
            </>
          ) : panel === "route" ? (
            <>
              <p>
                New runs through <code>stable</code> will use version 2. Existing runs keep their
                original version.
              </p>
              <Details
                values={[
                  ["Service", model.agentName],
                  ["Route", "stable"],
                  ["Published version", "2"],
                  [
                    "Current assignment",
                    model.routeAssigned ? "Version 2" : "None (version 1 retired)",
                  ],
                ]}
              />
              <Button variant="primary" onPress={() => finish({ type: "assign-route" })}>
                Confirm route assignment
              </Button>
            </>
          ) : panel === "invoke" ? (
            <>
              <Details
                values={[
                  ["Service", model.agentName],
                  ["Route", "stable"],
                  ["Version", "2"],
                ]}
              />
              <Field label="Analysis request" value={prompt} onChange={setPrompt} multiline />
              <p className="muted">
                The sample run demonstrates a region question followed by a publication approval.
              </p>
              <Button
                variant="primary"
                isDisabled={!prompt.trim()}
                onPress={() => finish({ type: "invoke" }, "/runs/agent-review")}
              >
                Start sample run
              </Button>
            </>
          ) : panel === "approval" ? (
            <>
              <Badge tone="warning">Decision required</Badge>
              <h3>Publish summary to workspace artifacts</h3>
              {scenario === "expired" && (
                <Notice title="The approval deadline has passed" tone="warning">
                  This request cannot accept a decision.
                </Notice>
              )}
              <p>
                This action retains a report for the selected regions. It does not send a message or
                export data outside the workspace.
              </p>
              <Details
                values={[
                  ["Requested by", model.agentName],
                  ["Regions", model.answer || "Not answered"],
                  ["Destination", "Retail operations / Artifacts"],
                  ["Authority", "Sample workspace publication grant"],
                ]}
              />
              <div className="actions">
                <Button
                  variant="danger"
                  isDisabled={model.run !== "approval" || scenario === "expired"}
                  onPress={() => finish({ type: "deny" })}
                >
                  Deny
                </Button>
                <Button
                  variant="primary"
                  isDisabled={model.run !== "approval" || scenario === "expired"}
                  onPress={() => finish({ type: "approve" }, "/runs/agent-review?tab=results")}
                >
                  Approve publication
                </Button>
              </div>
            </>
          ) : panel === "task" ? (
            <>
              <Badge tone={model.jobRepaired ? "success" : "critical"}>
                {model.jobRepaired ? "Succeeded after repair" : "Failed"}
              </Badge>
              <Details
                values={[
                  ["Job", "Daily revenue refresh"],
                  ["Task", "Validate totals"],
                  ["Source", "retail.commerce.orders"],
                  ["Duration", "4 s"],
                ]}
              />
              <h3>Task output</h3>
              <pre className="error-output">
                {model.jobRepaired
                  ? "Validation passed. 21,480 orders accepted.\nDownstream summary retained."
                  : "SCHEMA_VALIDATION_FAILED\nRequired column 'currency' is missing.\nPartition: 2026-10-29\nPublish summary was not started."}
              </pre>
              <h3>Recovery</h3>
              <p>
                In this sample, a corrected source partition is available. Repair retries validation
                and then publication; the completed read task is retained.
              </p>
              <Button
                variant="primary"
                isDisabled={model.jobRepaired}
                onPress={() => finish({ type: "repair-job" })}
              >
                Repair with corrected sample
              </Button>
              <div className="related">
                <Link to="/catalog/orders">Inspect the expected schema</Link>
                <Link to="/runs/daily-refresh?tab=activity">View task activity</Link>
              </div>
            </>
          ) : panel === "policy" ? (
            <>
              <h3>Commerce analyst access</h3>
              <Details
                values={[
                  ["Principals", "Retail analysts"],
                  ["Resource scope", "retail.commerce"],
                  ["Allowed action", "Read data"],
                  ["Authority source", "Reviewed sample catalog policy"],
                ]}
              />
              <p>
                The policy does not grant publication, Finance access, or administration. This is an
                illustrative summary, not a policy editor.
              </p>
              <Link to="/catalog/orders?tab=access">View table access</Link>
            </>
          ) : panel === "access" ? (
            <>
              <Badge tone="warning">Restricted scope</Badge>
              <h3>Finance data is not in your access scope</h3>
              <p>
                The sample identity can read commerce data and build services in Retail operations.
                Finance requires a separate reviewer role.
              </p>
              <Details
                values={[
                  ["Identity", "Alex Morgan"],
                  ["Requested resource", "finance catalog"],
                  ["Decision", "Denied"],
                  ["Required role", "Finance reviewer"],
                ]}
              />
              <p>
                Contact a workspace administrator to review access. This prototype does not submit
                an access request or change a policy.
              </p>
              <Link to="/admin?tab=audit">Inspect recorded decisions</Link>
            </>
          ) : panel === "runtime" || panel === "sql-runtime" ? (
            <>
              <h3>{panel === "sql-runtime" ? "Shared SQL" : "Legacy agent environment"}</h3>
              <Badge tone={panel === "sql-runtime" ? "success" : "warning"}>
                {panel === "sql-runtime" ? "Ready in sample" : "Degraded in sample"}
              </Badge>
              <Details
                values={[
                  ["Scope", "Retail operations"],
                  [
                    "Observed readiness",
                    panel === "sql-runtime"
                      ? "Sample checks passed"
                      : "Certification evidence expired",
                  ],
                  [
                    "New execution",
                    panel === "sql-runtime" ? "Available in sample" : "Blocked until checks pass",
                  ],
                  ["Existing work", "Retained; not silently deleted"],
                ]}
              />
              <p>
                Refresh and verify the accepted runtime evidence before enabling new execution. No
                credential, gateway address, or raw runtime endpoint is exposed here.
              </p>
              <Link to="/admin?tab=environments">Open environment list</Link>
            </>
          ) : panel === "catalog" ? (
            <>
              <h3>retail.commerce.orders</h3>
              <p>Completed and pending orders, with net revenue normalized to USD.</p>
              <Table
                label="Source columns"
                headers={["Column", "Type"]}
                rows={columns.map((row) => ({ key: row[0] as string, cells: [row[0], row[1]] }))}
              />
              <Link to="/catalog/orders?tab=sample-data">Open full table preview</Link>
            </>
          ) : panel === "discard" ? (
            <>
              <p>
                A newer sample version was saved while you were editing. Your local code is still
                available.
              </p>
              <h3>Your current cell</h3>
              <pre>{model.notebook}</pre>
              <h3>Saved sample cell</h3>
              <pre>{notebookCode}</pre>
              <Button
                variant="danger"
                onPress={() => {
                  dispatch({ type: "notebook-edit", value: notebookCode });
                  dispatch({ type: "notebook-save" });
                  setScenario("normal");
                  setPanel(null);
                }}
              >
                Discard local edits and load saved sample
              </Button>
            </>
          ) : panel === "cancel" ? (
            <>
              <p>
                Request cancellation of the current sample run. A cancellation request does not
                immediately confirm that execution has stopped.
              </p>
              <Button variant="danger" onPress={() => finish({ type: "cancel" })}>
                Request cancellation
              </Button>
            </>
          ) : (
            <>
              <p>
                Implementation metadata is available on demand, separate from the main reading path.
              </p>
              <Details
                values={[
                  ["Resource", `${route.area}/${route.id || "list"}`],
                  ["Workspace", "Retail operations (sample)"],
                  ["Identity", "Alex Morgan (sample)"],
                  ["Correlation", "sample-review-0042"],
                  ["Data source", "Local fictional fixtures"],
                ]}
              />
              <Button
                onPress={async () => {
                  try {
                    await navigator.clipboard.writeText(
                      `DataGround prototype: ${route.area}/${route.id || "list"}`,
                    );
                    setFeedback("Sample resource reference copied");
                    setPanel(null);
                  } catch {
                    setFeedback(
                      "Clipboard unavailable; the resource reference remains visible in Details",
                    );
                  }
                }}
              >
                Copy resource reference
              </Button>
            </>
          )}
        </Panel>
      )}
    </div>
  );
}
