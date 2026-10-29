import { useState } from "react";
import { columns, datasets, money, notebookCode, orders, resources, revenue } from "./data";
import type { Model } from "./model";
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
  Table,
  Tabs,
} from "./ui";

const runLabel = (model: Model) =>
  ({
    question: "Waiting for answer",
    approval: "Waiting for approval",
    succeeded: "Succeeded",
    denied: "Stopped: approval denied",
    cancelling: "Cancelling",
    cancelled: "Cancelled",
  })[model.run];
const runTone = (model: Model) =>
  model.run === "succeeded"
    ? "success"
    : ["question", "approval", "cancelling"].includes(model.run)
      ? "warning"
      : "neutral";
function RevenueTable() {
  return (
    <Table
      label="Revenue by region"
      headers={["Region", "Orders", "Net revenue (USD)"]}
      rows={revenue.map((row) => ({
        key: row.region,
        cells: [
          row.region,
          row.orders.toLocaleString("en-US"),
          <strong key="value">{money(row.revenue)}</strong>,
        ],
      }))}
    />
  );
}
function RevenueChart() {
  return (
    <figure className="bar-chart">
      <figcaption>
        October net revenue by region <span>USD</span>
      </figcaption>
      {revenue.map((row) => (
        <div className="bar-row" key={row.region}>
          <span>{row.region}</span>
          <div className="bar-track">
            <div style={{ width: `${(row.revenue / 284350) * 100}%` }} />
          </div>
          <strong>{money(row.revenue)}</strong>
        </div>
      ))}
      <p>Source: retail.commerce.orders · October 2026 · Fictional sample</p>
    </figure>
  );
}
function ResourceName({ name, kind, to }: { name: string; kind: string; to: string }) {
  return (
    <Link to={to} className="resource-name">
      <Icon
        name={
          kind === "Table"
            ? "catalog"
            : kind === "Notebook"
              ? "notebook"
              : kind === "SQL query"
                ? "sql"
                : kind === "Job"
                  ? "jobs"
                  : kind === "Agent service"
                    ? "agents"
                    : "artifacts"
        }
      />
      <span>{name}</span>
    </Link>
  );
}
export function ResourceTable({
  query = "",
  workspace = false,
  saved = false,
}: {
  query?: string;
  workspace?: boolean;
  saved?: boolean;
}) {
  const items = resources.filter(
    (item) =>
      (!workspace || ["SQL query", "Notebook"].includes(item.kind)) &&
      `${item.name} ${item.kind}`.toLowerCase().includes(query.toLowerCase()),
  );
  return (
    <Table
      label={workspace ? "Workspace files" : "Recent resources"}
      headers={["Name", "Type", "Location", "Last opened"]}
      rows={items.map((item) => ({
        key: item.to,
        cells: [
          <ResourceName key="name" {...item} />,
          item.kind,
          item.path,
          saved && item.kind === "SQL query" ? "Just saved" : item.updated,
        ],
      }))}
    />
  );
}
export function Home({ model }: PageProps) {
  return (
    <>
      <Header
        eyebrow="Workspace"
        title="Retail operations"
        description="Find data, continue an analysis, and review work that needs attention."
        actions={
          <Link className="primary-link" to="/sql/revenue">
            <Icon name="plus" /> New query
          </Link>
        }
      />
      <div className="home-layout">
        <div>
          <section className="home-section">
            <div className="section-title">
              <h2>Needs attention</h2>
              <Link to="/jobs?tab=runs">
                View runs <Icon name="arrow" size={14} />
              </Link>
            </div>
            {!model.jobRepaired && (
              <Link to="/runs/daily-refresh" className="attention-row">
                <span className="status-symbol critical">!</span>
                <div>
                  <strong>Daily revenue refresh failed</strong>
                  <p>Validate totals · Missing currency column · Today, 09:00</p>
                </div>
                <Icon name="chevron" />
              </Link>
            )}
            {["question", "approval"].includes(model.run) && (
              <Link to="/runs/agent-review" className="attention-row">
                <span className="status-symbol waiting">?</span>
                <div>
                  <strong>
                    {model.agentName} needs {model.run === "question" ? "an answer" : "approval"}
                  </strong>
                  <p>October revenue summary · {runLabel(model)}</p>
                </div>
                <Icon name="chevron" />
              </Link>
            )}
            {model.jobRepaired && !["question", "approval"].includes(model.run) && (
              <Notice title="You are all caught up" tone="success">
                No decisions are pending in this sample workspace.
              </Notice>
            )}
          </section>
          <section className="home-section">
            <div className="section-title">
              <h2>Recent work</h2>
              <Link to="/workspace">
                Open workspace <Icon name="arrow" size={14} />
              </Link>
            </div>
            <ResourceTable saved={model.savedQuery} />
          </section>
        </div>
        <aside className="home-aside">
          <h2>Start with your data</h2>
          <p>Explore the commerce catalog and follow a result from source to saved analysis.</p>
          <Link className="shortcut" to="/catalog/orders">
            <Icon name="catalog" />
            <span>
              Explore orders<small>Schema and sample data</small>
            </span>
            <Icon name="chevron" />
          </Link>
          <Link className="shortcut" to="/workspace/revenue-notebook">
            <Icon name="notebook" />
            <span>
              Revenue notebook<small>Code and results together</small>
            </span>
            <Icon name="chevron" />
          </Link>
          <Link className="shortcut" to="/agents/revenue">
            <Icon name="agents" />
            <span>
              Build an agent<small>Configure, publish and run</small>
            </span>
            <Icon name="chevron" />
          </Link>
          <hr />
          <h3>Workspace context</h3>
          <Details
            values={[
              ["Organization", "Northstar Retail"],
              ["Workspace", "Retail operations"],
              ["Sample identity", "Alex Morgan · Analyst & builder"],
            ]}
          />
        </aside>
      </div>
    </>
  );
}
function CatalogTree({ active = "" }: { active?: string }) {
  return (
    <aside className="resource-tree" aria-label="Catalog browser">
      <div className="tree-heading">
        <Icon name="catalog" /> Catalog explorer
      </div>
      <details open>
        <summary>retail</summary>
        <details open>
          <summary>commerce</summary>
          {datasets.map((item) => (
            <Link
              key={item.id}
              to={`/catalog/${item.id}`}
              className={active === item.id ? "selected" : ""}
            >
              <Icon name="catalog" size={15} />
              <span>{item.name}</span>
            </Link>
          ))}
        </details>
        <details>
          <summary>operations</summary>
          <Link to="/jobs/revenue-refresh">
            <Icon name="jobs" size={15} /> Daily revenue refresh
          </Link>
        </details>
      </details>
      <Link className="tree-restricted" to="/catalog/finance">
        <Icon name="admin" size={15} /> finance <small>Restricted</small>
      </Link>
      <div className="tree-footer">
        Catalogs are shared resources.
        <br />
        Access is checked per object.
      </div>
    </aside>
  );
}
export function Catalog({ route, inspect, query, setQuery }: PageProps) {
  const item = datasets.find((value) => value.id === route.id);
  if (item && item.id !== "orders")
    return (
      <div className="split-layout">
        <CatalogTree active={item.id} />
        <div className="split-content">
          <Header
            eyebrow={<Link to="/catalog">Catalog / retail / commerce</Link>}
            title={item.name}
            description={item.description}
          />
          <div className="content-pad">
            <Details
              values={[
                ["Type", item.type],
                ["Rows", item.rows],
                ["Updated", item.updated],
              ]}
            />
            <Notice title="Context sample">
              This object demonstrates catalog browsing and long content. The complete schema,
              preview and query journey is available for <Link to="/catalog/orders">orders</Link>.
            </Notice>
          </div>
        </div>
      </div>
    );
  return (
    <div className="split-layout">
      <CatalogTree active={route.id} />
      <div className="split-content">
        {route.id === "finance" ? (
          <>
            <Header
              eyebrow="Catalog"
              title="Finance"
              description="This data is restricted to Finance reviewers."
            />
            <div className="content-pad">
              <Notice title="Access denied" tone="warning">
                Your sample identity cannot read this catalog.{" "}
                <Button variant="quiet" onPress={() => inspect("access")}>
                  View access explanation
                </Button>
              </Notice>
            </div>
          </>
        ) : item ? (
          <>
            <Header
              eyebrow={
                <>
                  <Link to="/catalog">Catalog</Link>
                  <span>/</span>retail<span>/</span>commerce
                </>
              }
              title={item.name}
              description={item.description}
              actions={
                <Link className="primary-link" to="/sql/revenue">
                  Open in SQL <Icon name="arrow" size={16} />
                </Link>
              }
            />
            <Tabs
              base={`/catalog/${item.id}`}
              value={route.tab}
              items={["Overview", "Sample data", "Lineage", "Access"]}
            />
            {route.tab === "sample-data" ? (
              <div className="content-pad">
                <div className="section-title">
                  <h2>Sample data</h2>
                  <span className="muted">5 fictional rows · Read-only preview</span>
                </div>
                {item.id === "orders" ? (
                  <Table
                    label="Order sample"
                    headers={columns.map((value) => value[0] as string)}
                    rows={orders.map((row) => ({ key: row[0] as string, cells: row }))}
                  />
                ) : (
                  <Empty title="Sample not included">
                    This prototype provides row previews for orders.{" "}
                    <Link to="/catalog/orders?tab=sample-data">Open the orders sample</Link>.
                  </Empty>
                )}
              </div>
            ) : route.tab === "lineage" ? (
              <div className="content-pad">
                <h2>Source and downstream use</h2>
                <p className="muted">Declared relationships in the sample workspace.</p>
                <div className="lineage">
                  <Link to="/jobs/revenue-refresh">Daily revenue refresh</Link>
                  <Icon name="arrow" />
                  <strong>{item.name}</strong>
                  <Icon name="arrow" />
                  <Link to="/sql/revenue">Revenue by region</Link>
                </div>
                <Link to="/workspace/revenue-notebook">Open the connected analysis notebook</Link>
              </div>
            ) : route.tab === "access" ? (
              <div className="content-pad">
                <h2>Access to this table</h2>
                <Table
                  label="Table access"
                  headers={["Principal", "Access", "Source"]}
                  rows={[
                    {
                      key: "a",
                      cells: ["Retail analysts", "Read data", "Commerce catalog policy"],
                    },
                    {
                      key: "b",
                      cells: ["Revenue refresh service", "Publish data", "Reviewed workload grant"],
                    },
                  ]}
                />
                <Button variant="quiet" onPress={() => inspect("policy")}>
                  Inspect governing policy
                </Button>
              </div>
            ) : (
              <div className="detail-columns">
                <section>
                  <div className="section-title">
                    <h2>Columns</h2>
                    <span className="muted">
                      {item.id === "orders" ? "7 columns" : "Preview available for orders"}
                    </span>
                  </div>
                  {item.id === "orders" ? (
                    <Table
                      label="Order schema"
                      headers={["Column", "Type", "Description", "Nullable"]}
                      rows={columns.map((row) => ({
                        key: row[0] as string,
                        cells: [
                          <code key="name">{row[0]}</code>,
                          <code key="type">{row[1]}</code>,
                          row[2],
                          row[3],
                        ],
                      }))}
                    />
                  ) : (
                    <Empty title="Explore the reference table">
                      Use <Link to="/catalog/orders">orders</Link> to review the complete schema and
                      analysis journey.
                    </Empty>
                  )}
                  <div className="related">
                    <h3>Related work</h3>
                    <Link to="/sql/revenue">Revenue by region</Link>
                    <Link to="/workspace/revenue-notebook">October revenue analysis</Link>
                  </div>
                </section>
                <aside className="detail-aside">
                  <Badge tone="success">Available</Badge>
                  <Details
                    values={[
                      ["Type", item.type],
                      ["Rows", item.rows],
                      ["Updated", item.updated],
                      ["Steward", "Retail data team"],
                      [
                        "Source job",
                        <Link key="job" to="/jobs/revenue-refresh">
                          Daily revenue refresh
                        </Link>,
                      ],
                    ]}
                  />
                  <Button variant="quiet" onPress={() => inspect("metadata")}>
                    Technical details
                  </Button>
                </aside>
              </div>
            )}
          </>
        ) : (
          <>
            <Header
              eyebrow="Data"
              title="Catalog"
              description="Discover governed data and understand how it is used."
            />
            <div className="content-pad">
              <Filter value={query} onChange={setQuery} placeholder="Search catalog objects">
                <Badge>retail.commerce</Badge>
              </Filter>
              <Table
                label="Catalog objects"
                headers={["Name", "Type", "Description", "Rows", "Updated"]}
                rows={datasets
                  .filter((item) => item.name.includes(query.toLowerCase()))
                  .map((item) => ({
                    key: item.id,
                    cells: [
                      <ResourceName
                        key="name"
                        name={item.name}
                        kind="Table"
                        to={`/catalog/${item.id}`}
                      />,
                      item.type,
                      item.description,
                      item.rows,
                      item.updated,
                    ],
                  }))}
              />
            </div>
          </>
        )}
      </div>
    </div>
  );
}
export function Workspace({
  route,
  model,
  dispatch,
  inspect,
  query,
  setQuery,
  scenario,
}: PageProps) {
  if (!route.id)
    return (
      <>
        <Header
          eyebrow="Build and analyze"
          title="Workspace"
          description="Your notebooks, queries, and files in one place."
          actions={
            <Link className="primary-link" to="/workspace/revenue-notebook">
              <Icon name="plus" /> Open notebook
            </Link>
          }
        />
        <div className="content-pad">
          <div className="folder-strip">
            <Icon name="workspace" />
            <strong>Sales analytics</strong>
            <span className="muted">Shared workspace folder</span>
          </div>
          <Filter value={query} onChange={setQuery} placeholder="Search workspace files" />
          <ResourceTable workspace saved={model.savedQuery} query={query} />
        </div>
      </>
    );
  return (
    <div className="split-layout">
      <aside className="resource-tree" aria-label="Workspace files">
        <div className="tree-heading">
          <Link to="/workspace">
            <Icon name="workspace" /> Workspace
          </Link>
        </div>
        <details open>
          <summary>Sales analytics</summary>
          <Link className="selected" to="/workspace/revenue-notebook">
            <Icon name="notebook" size={15} /> October revenue analysis
          </Link>
          <Link to="/sql/revenue">
            <Icon name="sql" size={15} /> Revenue by region
          </Link>
        </details>
        <Button variant="quiet" onPress={() => inspect("catalog")}>
          <Icon name="catalog" /> Browse data
        </Button>
        <div className="tree-footer">
          Files and execution are separate.
          <br />
          Save the document to retain edits.
        </div>
      </aside>
      <div className="split-content">
        <div className="editor-tabs">
          <span className="active">
            <Icon name="notebook" size={15} /> October revenue analysis
          </span>
          <Link to="/sql/revenue">
            <Icon name="sql" size={15} /> Revenue by region
          </Link>
        </div>
        <Header
          eyebrow={<Link to="/workspace">Workspace / Sales analytics</Link>}
          title="October revenue analysis"
          actions={
            <>
              <span className="save-state">
                {model.notebookSaved ? "Saved" : "Unsaved changes"}
              </span>
              <Button
                isDisabled={scenario === "conflict"}
                onPress={() => dispatch({ type: "notebook-save" })}
              >
                Save
              </Button>
              <Button
                variant="primary"
                isDisabled={model.notebookStatus === "running"}
                onPress={() => dispatch({ type: "notebook-run" })}
              >
                Run all
              </Button>
            </>
          }
        />
        <div className="editor-toolbar">
          <Badge tone="success">Python · Ready</Badge>
          <span>Retail analysis environment</span>
          <Button variant="quiet" onPress={() => inspect("catalog")}>
            Browse source data
          </Button>
        </div>
        <div className="notebook-body">
          {scenario === "conflict" && (
            <Notice title="A newer notebook version exists" tone="warning">
              Your edits are retained.{" "}
              <Button onPress={() => inspect("discard")}>Review conflict</Button>
            </Notice>
          )}
          <div className="markdown-cell">
            <span className="cell-number">1</span>
            <h2>October revenue by region</h2>
            <p>
              Compare net revenue across the four sales regions. All amounts below are normalized to
              USD.
            </p>
            <Link to="/catalog/orders">Source: retail.commerce.orders</Link>
          </div>
          <div className="code-cell">
            <div className="cell-toolbar">
              <span>2 · Python</span>
              <span>
                {model.notebookStatus === "complete" ? "Completed · sample execution" : "Code"}
              </span>
            </div>
            <textarea
              aria-label="Notebook Python cell"
              spellCheck={false}
              value={model.notebook}
              onChange={(event) => dispatch({ type: "notebook-edit", value: event.target.value })}
            />
          </div>
          {model.notebookStatus === "running" && (
            <Notice title="Running sample cell…">Execution is simulated in this prototype.</Notice>
          )}
          {model.notebookStatus === "unsupported" && (
            <Notice title="This cell cannot run in the prototype" tone="warning">
              Only the provided sample is supported.{" "}
              <Button onPress={() => dispatch({ type: "notebook-edit", value: notebookCode })}>
                Restore sample cell
              </Button>
            </Notice>
          )}
          {model.notebookStatus === "complete" && (
            <div className="cell-output">
              <RevenueTable />
              <RevenueChart />
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
export function SQL({ route, model, dispatch, inspect, scenario }: PageProps) {
  return (
    <div className="split-layout">
      <CatalogTree />
      <div className="split-content">
        <div className="editor-tabs">
          <Link to="/workspace/revenue-notebook">
            <Icon name="notebook" size={15} /> October revenue analysis
          </Link>
          <span className="active">
            <Icon name="sql" size={15} /> Revenue by region
          </span>
        </div>
        <Header
          eyebrow="SQL / Sales analytics"
          title="Revenue by region"
          actions={
            <>
              <span className="save-state">
                {model.savedQuery ? "Saved in Workspace" : "Draft query"}
              </span>
              <Button onPress={() => dispatch({ type: "query-save" })}>Save query</Button>
              {model.queryStatus === "running" ? (
                <Button onPress={() => dispatch({ type: "query-cancel" })}>Cancel query</Button>
              ) : (
                <Button variant="primary" onPress={() => dispatch({ type: "query-run" })}>
                  Run query
                </Button>
              )}
            </>
          }
        />
        <div className="editor-toolbar">
          <span>
            Catalog <strong>retail</strong>
          </span>
          <span>
            Schema <strong>commerce</strong>
          </span>
          <Badge tone="success">Shared SQL · Ready</Badge>
          <Button variant="quiet" onPress={() => inspect("sql-runtime")}>
            Environment details
          </Button>
        </div>
        <div className="sql-editor">
          <div className="line-numbers" aria-hidden="true">
            {Array.from({ length: model.query.split("\n").length }, (_, i) => i + 1).map((line) => (
              <span key={line}>{line}</span>
            ))}
          </div>
          <textarea
            aria-label="SQL query"
            spellCheck={false}
            value={model.query}
            onChange={(event) => dispatch({ type: "query-edit", value: event.target.value })}
          />
        </div>
        <div className="editor-status">
          <span>SQL</span>
          <span>Fictional dataset · Sample execution only</span>
        </div>
        <Tabs base="/sql/revenue" value={route.tab} items={["Results", "Chart", "History"]} />
        <div className="result-area">
          {model.queryStatus === "running" ? (
            <Notice title="Running sample query…">
              The query can be cancelled while it is running.
            </Notice>
          ) : model.queryStatus === "unsupported" ? (
            <Notice title="The prototype does not execute arbitrary SQL" tone="warning">
              Your edits are retained. Restore the example to continue the connected journey.{" "}
              <Button onPress={() => dispatch({ type: "query-reset" })}>
                Restore example query
              </Button>
            </Notice>
          ) : scenario === "failed" ? (
            <Notice title="Query failed" tone="critical">
              The selected environment is unavailable. Your query has been retained.{" "}
              <Button onPress={() => inspect("runtime")}>Inspect environment</Button>
            </Notice>
          ) : route.tab === "history" ? (
            <Table
              label="Query history"
              headers={["Query", "State", "Rows", "Duration", "Source"]}
              rows={
                model.queryRuns
                  ? [
                      {
                        key: "latest",
                        cells: [
                          "Revenue by region",
                          <Badge key="state" tone="success">
                            Succeeded
                          </Badge>,
                          "4",
                          "0.8 s",
                          `${model.queryRuns} sample execution${model.queryRuns === 1 ? "" : "s"}`,
                        ],
                      },
                    ]
                  : []
              }
              empty="Run the example query to see its execution history."
            />
          ) : model.queryStatus === "complete" ? (
            <>
              <div className="section-title">
                <h2>{route.tab === "chart" ? "Revenue comparison" : "Query results"}</h2>
                <span className="muted">4 rows · 0.8 s · Simulated</span>
              </div>
              {route.tab === "chart" ? <RevenueChart /> : <RevenueTable />}
              <div className="result-links">
                <Link to="/workspace">View saved work</Link>
                <Link to="/catalog/orders">Inspect source table</Link>
              </div>
            </>
          ) : (
            <Empty
              title={model.queryStatus === "cancelled" ? "Query cancelled" : "Ready to explore"}
            >
              Run the example query to compare October revenue across regions.
            </Empty>
          )}
        </div>
      </div>
    </div>
  );
}

export function Agents({ route, model, dispatch, inspect, query, setQuery }: PageProps) {
  if (route.id === "operations-assistant")
    return (
      <>
        <Header
          eyebrow={<Link to="/agents">Agents / Persistent assistants</Link>}
          title="Operations assistant"
          description="A persistent assistant with its own memory, schedules, skills, and channels."
        />
        <Tabs
          base="/agents/operations-assistant"
          value={route.tab}
          items={["Overview", "Memory", "Skills", "Schedules", "Channels"]}
        />
        <div className="content-pad">
          <Badge tone="success">Available · Fictional profile</Badge>
          <h2>
            {route.tab === "overview"
              ? "Assistant profile"
              : route.tab[0]?.toUpperCase() + route.tab.slice(1)}
          </h2>
          <Table
            label="Assistant configuration"
            headers={["Item", "State", "Details"]}
            rows={
              route.tab === "memory"
                ? [
                    {
                      key: "m",
                      cells: [
                        "Operations preferences",
                        "Retained",
                        "Weekly report uses the retail calendar",
                      ],
                    },
                  ]
                : route.tab === "skills"
                  ? [
                      {
                        key: "s",
                        cells: ["Revenue reporting", "Version 2", "Reviewed skill template"],
                      },
                    ]
                  : route.tab === "schedules"
                    ? [
                        {
                          key: "s",
                          cells: [
                            "Weekly operations summary",
                            "Paused",
                            "Mondays, 08:00 · Europe/Stockholm",
                          ],
                        },
                      ]
                    : route.tab === "channels"
                      ? [
                          {
                            key: "c",
                            cells: ["Workbench", "Enabled", "Interactive questions and approvals"],
                          },
                          {
                            key: "e",
                            cells: [
                              "External messaging",
                              "Not configured",
                              "Requires a separately authorized channel",
                            ],
                          },
                        ]
                      : [
                          {
                            key: "p",
                            cells: [
                              "Profile",
                              "Persistent",
                              "Memory and schedule state outlive individual runs",
                            ],
                          },
                          {
                            key: "a",
                            cells: [
                              "Authority",
                              "Scoped",
                              "Decisions remain subject to current policy",
                            ],
                          },
                        ]
            }
          />
        </div>
      </>
    );
  if (!route.id)
    return (
      <>
        <Header
          eyebrow="Build and operate"
          title="Agents"
          description="Reusable services and persistent assistants with clear execution boundaries."
          actions={
            <Button variant="primary" onPress={() => inspect("new-agent")}>
              <Icon name="plus" /> New agent service
            </Button>
          }
        />
        <Tabs base="/agents" value={route.tab} items={["Services", "Persistent assistants"]} />
        <div className="content-pad">
          <Filter value={query} onChange={setQuery} placeholder="Search agents" />
          {route.tab === "persistent-assistants" ? (
            <Table
              label="Persistent assistants"
              headers={["Name", "State", "Schedule", "Channels"]}
              rows={
                "operations assistant".includes(query.toLowerCase())
                  ? [
                      {
                        key: "assistant",
                        cells: [
                          <ResourceName
                            key="n"
                            name="Operations assistant"
                            kind="Agent service"
                            to="/agents/operations-assistant"
                          />,
                          <Badge key="s" tone="success">
                            Available
                          </Badge>,
                          "1 paused schedule",
                          "Workbench",
                        ],
                      },
                    ]
                  : []
              }
            />
          ) : (
            <Table
              label="Agent services"
              headers={["Name", "Published version", "Route", "Last run", "Updated"]}
              rows={
                model.agentName.toLowerCase().includes(query.toLowerCase())
                  ? [
                      {
                        key: "revenue",
                        cells: [
                          <ResourceName
                            key="n"
                            name={model.agentName}
                            kind="Agent service"
                            to="/agents/revenue"
                          />,
                          model.publication === "published"
                            ? "Version 2"
                            : model.publication === "uncertain"
                              ? "Unknown"
                              : "Version 1 retired · Version 2 draft",
                          model.routeAssigned ? "stable → Version 2" : "Version 2 not assigned",
                          model.runStarted ? (
                            <Link key="r" to="/runs/agent-review">
                              {runLabel(model)}
                            </Link>
                          ) : (
                            "Not run"
                          ),
                          "Today, 09:30",
                        ],
                      },
                    ]
                  : []
              }
            />
          )}
        </div>
      </>
    );
  return (
    <>
      <Header
        eyebrow={<Link to="/agents">Agents / Services</Link>}
        title={model.agentName}
        description="Create a governed revenue summary from the commerce dataset."
        actions={
          <>
            <Badge tone={model.publication === "published" ? "success" : "warning"}>
              {model.publication === "published"
                ? "Published"
                : model.publication === "uncertain"
                  ? "Outcome unknown"
                  : "Draft"}
            </Badge>
            <Button
              variant="primary"
              isDisabled={!model.routeAssigned}
              onPress={() => inspect("invoke")}
            >
              Run agent
            </Button>
          </>
        }
      />
      <Tabs
        base="/agents/revenue"
        value={route.tab}
        items={["Overview", "Build", "Versions", "Deployments", "Runs"]}
      />
      <div className="content-pad">
        {model.publication === "uncertain" && (
          <Notice title="Publication outcome is unknown" tone="warning">
            The exact request is retained. Do not submit a different version.{" "}
            <Button onPress={() => dispatch({ type: "publication-observe" })}>
              Check publication state
            </Button>
          </Notice>
        )}
        {route.tab === "build" ? (
          <div className="form-layout">
            <section>
              <h2>Service definition</h2>
              <Field
                label="Service name"
                value={model.agentName}
                disabled={model.publication !== "draft"}
                onChange={(name) => dispatch({ type: "agent-edit", name, purpose: model.purpose })}
              />
              <Field
                label="Instructions"
                value={model.purpose}
                multiline
                disabled={model.publication !== "draft"}
                onChange={(purpose) =>
                  dispatch({ type: "agent-edit", name: model.agentName, purpose })
                }
              />
              <div className="field">
                <span>Interaction input</span>
                <div className="input-preview">
                  <strong>Analysis request</strong>
                  <p>One required text prompt. Ask for the report you need.</p>
                  <code>prompt: string</code>
                </div>
              </div>
              <div className="actions">
                <Link to="/agents/revenue?tab=versions">
                  Review version <Icon name="arrow" size={15} />
                </Link>
              </div>
            </section>
            <aside className="detail-aside">
              <h3>Runtime & access</h3>
              <Details
                values={[
                  ["Runtime profile", "Governed analyst"],
                  ["Data access", "retail.commerce · read"],
                  ["Output destination", "Workspace artifacts"],
                  ["Publication", "Approval required"],
                ]}
              />
              <p className="muted">
                The sample profile illustrates a reviewed capability set. It does not grant access
                to a live runtime.
              </p>
            </aside>
          </div>
        ) : route.tab === "versions" ? (
          <>
            <div className="section-title">
              <h2>Version history</h2>
              <Button
                variant="primary"
                isDisabled={model.publication !== "draft"}
                onPress={() => inspect("publish")}
              >
                Review publication
              </Button>
            </div>
            <Table
              label="Agent versions"
              headers={["Version", "State", "Created by", "Input", "Output"]}
              rows={[
                {
                  key: "v2",
                  cells: [
                    "Version 2",
                    <Badge key="s" tone={model.publication === "published" ? "success" : "warning"}>
                      {model.publication}
                    </Badge>,
                    "Alex Morgan",
                    "Analysis request",
                    "Summary and regional revenue",
                  ],
                },
                {
                  key: "v1",
                  cells: [
                    "Version 1",
                    <Badge key="retired">Retired</Badge>,
                    "Alex Morgan",
                    "Analysis request",
                    <Link key="history" to="/runs/previous-summary">
                      Inspect previous output
                    </Link>,
                  ],
                },
              ]}
            />
            <div className="review-contract">
              <h3>What this version does</h3>
              <p>{model.purpose}</p>
              <details>
                <summary>Inspect input and output contracts</summary>
                <pre>
                  {JSON.stringify(
                    {
                      input: { prompt: "required string" },
                      output: { summary: "text", regionalRevenue: "artifact reference" },
                    },
                    null,
                    2,
                  )}
                </pre>
              </details>
            </div>
            {model.publication === "published" && (
              <Link className="primary-link" to="/agents/revenue?tab=deployments">
                Assign a deployment route <Icon name="arrow" />
              </Link>
            )}
          </>
        ) : route.tab === "deployments" ? (
          <>
            <div className="section-title">
              <h2>Deployment routes</h2>
              <Button
                variant="primary"
                isDisabled={model.publication !== "published"}
                onPress={() => inspect("route")}
              >
                {model.routeAssigned ? "Review route" : "Assign route"}
              </Button>
            </div>
            {model.publication !== "published" && (
              <Notice title="Publish a version first">
                A route can only select a published version.{" "}
                <Link to="/agents/revenue?tab=versions">Review version 2</Link>.
              </Notice>
            )}
            <Table
              label="Deployment routes"
              headers={["Route", "Version", "Observed state", "Updated"]}
              rows={
                model.routeAssigned
                  ? [
                      {
                        key: "stable",
                        cells: [
                          "stable",
                          "Version 2",
                          <Badge key="s" tone="success">
                            Assigned
                          </Badge>,
                          "Just now",
                        ],
                      },
                    ]
                  : []
              }
              empty="No route is assigned. Publication and routing are separate actions."
            />
          </>
        ) : route.tab === "runs" ? (
          <RunTable model={model} onlyAgent />
        ) : (
          <div className="detail-columns">
            <section>
              <h2>Ready for the next step</h2>
              <ol className="setup-list">
                <li>
                  <span className="step-marker">1</span>
                  <div>
                    <strong>Define the service</strong>
                    <p>Review instructions, input, and runtime access.</p>
                    <Link to="/agents/revenue?tab=build">Open build settings</Link>
                  </div>
                </li>
                <li>
                  <span className="step-marker">2</span>
                  <div>
                    <strong>
                      {model.publication === "published"
                        ? "Version 2 is published"
                        : "Review and publish version 2"}
                    </strong>
                    <p>Publication freezes this service definition.</p>
                    <Link to="/agents/revenue?tab=versions">Review versions</Link>
                  </div>
                </li>
                <li>
                  <span className="step-marker">3</span>
                  <div>
                    <strong>
                      {model.routeAssigned
                        ? "stable selects version 2"
                        : "Choose the version new runs use"}
                    </strong>
                    <p>A route selects a published version for new work.</p>
                    <Link to="/agents/revenue?tab=deployments">Manage deployment routes</Link>
                  </div>
                </li>
              </ol>
            </section>
            <aside className="detail-aside">
              <h3>Service context</h3>
              <Details
                values={[
                  ["Workspace", "Retail operations"],
                  ["Created by", "Alex Morgan"],
                  ["Current version", "Version 2"],
                  ["Route", model.routeAssigned ? "stable" : "Version 2 not assigned"],
                ]}
              />
              <Button variant="quiet" onPress={() => inspect("metadata")}>
                Technical details
              </Button>
            </aside>
          </div>
        )}
      </div>
    </>
  );
}

function RunTable({
  model,
  onlyAgent = false,
  filter = "all",
}: {
  model: Model;
  onlyAgent?: boolean;
  filter?: string;
}) {
  const rows = [
    ...(!onlyAgent
      ? [
          {
            key: "job",
            state: model.jobRepaired ? "succeeded" : "failed",
            cells: [
              <Link key="n" to="/runs/daily-refresh">
                Daily revenue refresh
              </Link>,
              "Job",
              <Badge key="s" tone={model.jobRepaired ? "success" : "critical"}>
                {model.jobRepaired ? "Succeeded after repair" : "Failed"}
              </Badge>,
              "Today, 09:00",
              model.jobRepaired ? "47 s" : "22 s",
              "Schedule",
            ],
          },
        ]
      : []),
    {
      key: "agent",
      state: ["question", "approval"].includes(model.run) ? "waiting" : model.run,
      cells: [
        <Link key="n" to="/runs/agent-review">
          October revenue summary
        </Link>,
        "Agent",
        <Badge key="s" tone={runTone(model)}>
          {runLabel(model)}
        </Badge>,
        model.runStarted ? "Just now" : "Today, 09:30",
        "Waiting / observed",
        "Alex Morgan",
      ],
    },
    {
      key: "previous",
      state: "succeeded",
      cells: [
        <Link key="n" to="/runs/previous-summary">
          October revenue summary (previous run)
        </Link>,
        "Agent",
        <Badge key="s" tone="success">
          Succeeded
        </Badge>,
        "Yesterday, 16:30",
        "1 m 12 s",
        "Alex Morgan",
      ],
    },
  ].filter((row) => filter === "all" || row.state === filter);
  return (
    <Table
      label="Run history"
      headers={["Run", "Type", "State", "Started", "Duration", "Started by"]}
      rows={rows}
    />
  );
}
export function Jobs({ route, model, inspect, query, setQuery }: PageProps) {
  const [filter, setFilter] = useState("all");
  if (route.id)
    return (
      <>
        <Header
          eyebrow={<Link to="/jobs">Jobs & Runs / Jobs</Link>}
          title="Daily revenue refresh"
          description="Prepare and publish the daily commerce revenue table."
          actions={
            <Link className="primary-link" to="/runs/daily-refresh">
              Inspect latest run <Icon name="arrow" />
            </Link>
          }
        />
        <Tabs
          base="/jobs/revenue-refresh"
          value={route.tab}
          items={["Overview", "Runs", "Tasks", "Configuration"]}
        />
        <div className="content-pad">
          {route.tab === "runs" ? (
            <RunTable model={model} />
          ) : route.tab === "configuration" ? (
            <Details
              values={[
                ["Schedule", "Daily at 09:00 · Europe/Stockholm"],
                ["Execution identity", "Revenue refresh service"],
                ["Source", "Commerce orders"],
                ["Target", "retail.commerce.orders"],
                ["Failure notification", "Workspace attention list"],
              ]}
            />
          ) : (
            <>
              <div className="section-title">
                <h2>{route.tab === "tasks" ? "Task dependencies" : "Latest execution"}</h2>
                <Badge tone={model.jobRepaired ? "success" : "critical"}>
                  {model.jobRepaired ? "Succeeded after repair" : "Failed at Validate totals"}
                </Badge>
              </div>
              <TaskGraph repaired={model.jobRepaired} inspect={inspect} />
              <Link to="/runs/daily-refresh">Open run details and recovery</Link>
              <div className="related">
                <h3>Published dataset</h3>
                <Link to="/catalog/orders">retail.commerce.orders</Link>
              </div>
            </>
          )}
        </div>
      </>
    );
  return (
    <>
      <Header
        eyebrow="Operate"
        title="Jobs & Runs"
        description="Manage scheduled work and inspect executions across the workspace."
      />
      <Tabs base="/jobs" value={route.tab} items={["Jobs", "Runs"]} />
      <div className="content-pad">
        {route.tab === "runs" ? (
          <>
            <div className="filterbar">
              <label className="select-label">
                Run state
                <select value={filter} onChange={(e) => setFilter(e.target.value)}>
                  <option value="all">All states</option>
                  <option value="failed">Failed</option>
                  <option value="waiting">Waiting for input</option>
                  <option value="succeeded">Succeeded</option>
                </select>
              </label>
              <span className="muted">Workspace sample · Latest executions</span>
            </div>
            <RunTable model={model} filter={filter} />
          </>
        ) : (
          <>
            <Filter value={query} onChange={setQuery} placeholder="Search jobs" />
            <Table
              label="Jobs"
              headers={["Name", "Schedule", "Latest run", "Next run", "Created by"]}
              rows={
                "daily revenue refresh".includes(query.toLowerCase())
                  ? [
                      {
                        key: "daily",
                        cells: [
                          <ResourceName
                            key="n"
                            name="Daily revenue refresh"
                            kind="Job"
                            to="/jobs/revenue-refresh"
                          />,
                          "Daily · 09:00",
                          <Link key="r" to="/runs/daily-refresh">
                            <Badge tone={model.jobRepaired ? "success" : "critical"}>
                              {model.jobRepaired ? "Succeeded" : "Failed"}
                            </Badge>
                          </Link>,
                          "Tomorrow, 09:00",
                          "Retail data team",
                        ],
                      },
                    ]
                  : []
              }
            />
          </>
        )}
      </div>
    </>
  );
}
function TaskGraph({
  repaired,
  inspect,
}: {
  repaired: boolean;
  inspect: (panel: Inspector) => void;
}) {
  return (
    <section
      className="task-graph"
      aria-label="Task dependencies: Read orders, then Validate totals, then Publish summary"
    >
      <div className="task-node success">
        <Icon name="catalog" />
        <strong>Read orders</strong>
        <span>Succeeded · 18 s</span>
      </div>
      <Icon name="arrow" />
      <button
        type="button"
        className={`task-node ${repaired ? "success" : "failure"}`}
        onClick={() => inspect("task")}
      >
        <Icon name="sql" />
        <strong>Validate totals</strong>
        <span>{repaired ? "Succeeded · 4 s" : "Failed · 4 s"}</span>
        <small>Inspect task</small>
      </button>
      <Icon name="arrow" />
      <div className={`task-node ${repaired ? "success" : ""}`}>
        <Icon name="artifacts" />
        <strong>Publish summary</strong>
        <span>{repaired ? "Succeeded · 25 s" : "Not run · Dependency failed"}</span>
      </div>
    </section>
  );
}

export function Runs({ route, model, dispatch, inspect, scenario }: PageProps) {
  const [answer, setAnswer] = useState("");
  const [showResult, setShowResult] = useState(false);
  const job = route.id === "daily-refresh";
  const historical = route.id === "previous-summary";
  const complete = job ? model.jobRepaired : historical || model.run === "succeeded";
  const title = job
    ? "Daily revenue refresh"
    : historical
      ? "October revenue summary (previous run)"
      : "October revenue summary";
  const artifact = historical ? "revenue-summary" : job ? "job-summary" : "agent-summary";
  const tab = route.tab;
  const waiting = !job && !historical && ["question", "approval"].includes(model.run);
  const activity = [
    "Run accepted",
    "Read commerce schema",
    "Calculate regional revenue",
    ...(historical
      ? ["Summary retained"]
      : model.run === "question"
        ? ["Waiting for region selection"]
        : model.answer
          ? [`Answer recorded: ${model.answer}`]
          : []),
    ...(!historical && model.run === "succeeded"
      ? ["Publication approved", "Summary retained"]
      : !historical && model.run === "approval"
        ? ["Waiting for publication approval"]
        : []),
  ];
  return (
    <>
      <Header
        eyebrow={
          <>
            <Link to={job ? "/jobs/revenue-refresh" : "/agents/revenue?tab=runs"}>
              {job ? "Jobs / Daily revenue refresh" : `Agents / ${model.agentName}`}
            </Link>
            <span>/</span>Run {job ? "184" : historical ? "42" : "43"}
          </>
        }
        title={title}
        description={
          job
            ? "Started today at 09:00 by schedule"
            : historical
              ? "Completed yesterday at 16:30"
              : `Started by Alex Morgan · Version ${model.runStarted ? "2" : "1"} · stable`
        }
        actions={
          <>
            <Badge tone={complete ? "success" : job ? "critical" : runTone(model)}>
              {complete ? "Succeeded" : job ? "Failed" : runLabel(model)}
            </Badge>
            {waiting && <Button onPress={() => inspect("cancel")}>Cancel run</Button>}
            <Button variant="quiet" onPress={() => inspect("metadata")}>
              Details
            </Button>
          </>
        }
      />
      {!job && !historical && model.run === "question" && (
        <div className="decision-band">
          <div>
            <Badge tone="warning">Answer needed</Badge>
            <h2>Which regions should the summary cover?</h2>
            <p>The agent is waiting. No publication action has been taken.</p>
          </div>
          <div className="decision-controls">
            <label>
              Regions
              <select
                aria-label="Regions to analyze"
                value={answer}
                onChange={(event) => setAnswer(event.target.value)}
                disabled={scenario === "expired"}
              >
                <option value="">Choose regions</option>
                <option>All regions</option>
              </select>
            </label>
            <Button
              variant="primary"
              isDisabled={!answer || scenario === "expired"}
              onPress={() =>
                dispatch({ type: "answer", value: answer, expired: scenario === "expired" })
              }
            >
              Submit answer
            </Button>
          </div>
          {scenario === "expired" && (
            <Notice title="The answer deadline has passed" tone="warning">
              The request remains visible, but cannot accept an answer.
            </Notice>
          )}
        </div>
      )}
      {!job && !historical && model.run === "approval" && (
        <div className="decision-band">
          <div>
            <Badge tone="warning">Approval needed</Badge>
            <h2>Publish the revenue summary to workspace artifacts?</h2>
            <p>
              Requested scope: {model.answer}. Review the destination and effect before deciding.
            </p>
          </div>
          <Button variant="primary" onPress={() => inspect("approval")}>
            Review approval
          </Button>
        </div>
      )}
      {!job && !historical && model.run === "cancelling" && (
        <div className="content-pad">
          <Notice title="Cancellation requested" tone="warning">
            Execution has not yet been confirmed stopped.{" "}
            <Button onPress={() => dispatch({ type: "cancel-complete" })}>
              Observe stopped sample
            </Button>
          </Notice>
        </div>
      )}
      <Tabs
        base={`/runs/${route.id}`}
        value={tab}
        items={["Overview", "Activity", "Results", "Files", "Audit"]}
      />
      <div className="content-pad">
        {tab === "activity" ? (
          job ? (
            <Table
              label="Task activity"
              headers={["Task", "State", "Duration", "Details"]}
              rows={[
                ["Read orders", "Succeeded", "18 s"],
                ["Validate totals", model.jobRepaired ? "Succeeded" : "Failed", "4 s"],
                [
                  "Publish summary",
                  model.jobRepaired ? "Succeeded" : "Not run",
                  model.jobRepaired ? "25 s" : "—",
                ],
              ].map((row) => ({
                key: row[0] as string,
                cells: [
                  ...row,
                  row[0] === "Validate totals" ? (
                    <Button key="detail" variant="quiet" onPress={() => inspect("task")}>
                      Inspect task
                    </Button>
                  ) : (
                    "—"
                  ),
                ],
              }))}
            />
          ) : (
            <div className="activity-list">
              {activity.map((value, i) => (
                <div key={value}>
                  <span className="activity-index">{i + 1}</span>
                  <div>
                    <strong>{value}</strong>
                    <p>
                      {i === 0
                        ? "Platform lifecycle"
                        : i === 1
                          ? "Tool · Catalog read"
                          : i === 2
                            ? "Tool · SQL analysis"
                            : "Interaction event"}
                    </p>
                  </div>
                  <Button variant="quiet" onPress={() => inspect(i === 1 ? "catalog" : "metadata")}>
                    Inspect
                  </Button>
                </div>
              ))}
            </div>
          )
        ) : tab === "results" ? (
          complete ? (
            <>
              <div className="section-title">
                <h2>Regional revenue summary</h2>
                <Button onPress={() => setShowResult((value) => !value)}>
                  {showResult ? "Hide result" : "Show result"}
                </Button>
              </div>
              {showResult ? (
                <>
                  <p>
                    Europe contributed the largest share of revenue in this fictional dataset.
                    Review the breakdown and retained artifact for provenance.
                  </p>
                  <RevenueTable />
                </>
              ) : (
                <Notice title="Result content is not loaded">
                  Reveal the sample result explicitly. Production reads require current resource
                  access.
                </Notice>
              )}
              <div className="result-links">
                <Link to={`/artifacts/${artifact}`}>Open retained summary</Link>
                <Link to="/catalog/orders">Inspect source table</Link>
              </div>
            </>
          ) : (
            <Empty title="No completed result">
              {job
                ? "Resolve the failed task before a result can be published."
                : "This run has not completed successfully. Required decisions appear above."}
            </Empty>
          )
        ) : tab === "files" ? (
          <Table
            label="Run artifacts"
            headers={["Name", "Type", "Retention", "Access"]}
            rows={
              complete
                ? [
                    {
                      key: "summary",
                      cells: [
                        <Link key="name" to={`/artifacts/${artifact}`}>
                          Revenue summary
                        </Link>,
                        "Markdown report",
                        "Retained",
                        "Restricted content",
                      ],
                    },
                  ]
                : []
            }
            empty="This run has not retained an output yet."
          />
        ) : tab === "audit" ? (
          <Table
            label="Run audit"
            headers={["Event", "Actor", "Decision", "Resource"]}
            rows={[
              {
                key: "entry",
                cells: [
                  "Run requested",
                  job ? "Scheduler" : "Alex Morgan",
                  "Allowed",
                  job ? "Daily revenue refresh" : model.agentName,
                ],
              },
              {
                key: "read",
                cells: ["Source read", "Governed workload", "Allowed", "retail.commerce.orders"],
              },
            ]}
          />
        ) : (
          <>
            <div className="section-title">
              <h2>{job ? "Execution path" : "Run summary"}</h2>
              <Link to={`/runs/${route.id}?tab=activity`}>
                View activity <Icon name="arrow" size={15} />
              </Link>
            </div>
            {job ? (
              <>
                <TaskGraph repaired={model.jobRepaired} inspect={inspect} />
                {!model.jobRepaired && (
                  <Notice title="Validate totals failed" tone="critical">
                    The source partition is missing the currency column. Downstream publication did
                    not run.{" "}
                    <Button onPress={() => inspect("task")}>Inspect failure and recovery</Button>
                  </Notice>
                )}
              </>
            ) : (
              <div className="run-overview">
                <div>
                  <h3>
                    {complete
                      ? "The summary is ready"
                      : model.run === "denied"
                        ? "Publication was denied"
                        : model.run === "cancelled"
                          ? "The run was cancelled"
                          : model.run === "cancelling"
                            ? "Cancellation is in progress"
                            : "Analysis is waiting for a decision"}
                  </h3>
                  <p>
                    {complete
                      ? "The result and its source references have been retained. Open the result to inspect the regional breakdown."
                      : "The execution context remains available while you review the current state."}
                  </p>
                  {complete && (
                    <Link className="primary-link" to={`/runs/${route.id}?tab=results`}>
                      Inspect result <Icon name="arrow" />
                    </Link>
                  )}
                </div>
                <Details
                  values={[
                    [
                      "Service",
                      <Link key="service" to="/agents/revenue">
                        {model.agentName}
                      </Link>,
                    ],
                    ["Version", historical || !model.runStarted ? "Version 1" : "Version 2"],
                    [
                      "Source",
                      <Link key="source" to="/catalog/orders">
                        retail.commerce.orders
                      </Link>,
                    ],
                    ["Output", complete ? "Revenue summary · retained" : "Not yet retained"],
                  ]}
                />
              </div>
            )}
          </>
        )}
      </div>
    </>
  );
}

export function Artifacts({ route, model, query, setQuery, inspect }: PageProps) {
  const [revealed, setRevealed] = useState(false);
  const origin =
    route.id === "agent-summary"
      ? "agent-review"
      : route.id === "job-summary"
        ? "daily-refresh"
        : "previous-summary";
  const runNumber = origin === "agent-review" ? "43" : origin === "daily-refresh" ? "184" : "42";
  const originName = origin === "daily-refresh" ? "Daily revenue refresh" : model.agentName;
  if (
    (origin === "agent-review" && model.run !== "succeeded") ||
    (origin === "daily-refresh" && !model.jobRepaired)
  )
    return (
      <>
        <Header title="Output not retained" />
        <Empty title="No completed output">
          Return to the <Link to={`/runs/${origin}`}>originating run</Link> to inspect its state.
        </Empty>
      </>
    );
  if (route.id)
    return (
      <>
        <Header
          eyebrow={<Link to="/artifacts">Artifacts</Link>}
          title={origin === "daily-refresh" ? "Daily revenue summary" : "October revenue summary"}
          description="A retained report with its source and execution context."
          actions={<Badge tone="success">Retained</Badge>}
        />
        <Tabs
          base={`/artifacts/${route.id}`}
          value={route.tab}
          items={["Preview", "Provenance", "Retention"]}
        />
        <div className="detail-columns">
          <section>
            {route.tab === "provenance" ? (
              <>
                <h2>Where this output came from</h2>
                <div className="lineage">
                  <Link to="/catalog/orders">orders</Link>
                  <Icon name="arrow" />
                  <Link to={`/runs/${origin}`}>
                    {originName} / Run {runNumber}
                  </Link>
                  <Icon name="arrow" />
                  <strong>Summary</strong>
                </div>
                <Details
                  values={[
                    [
                      "Source dataset",
                      <Link key="s" to="/catalog/orders">
                        retail.commerce.orders
                      </Link>,
                    ],
                    [
                      "Producing run",
                      <Link key="r" to={`/runs/${origin}`}>
                        {originName} · Run {runNumber}
                      </Link>,
                    ],
                    [
                      "Source definition",
                      <Link
                        key="v"
                        to={
                          origin === "daily-refresh"
                            ? "/jobs/revenue-refresh"
                            : "/agents/revenue?tab=versions"
                        }
                      >
                        {origin === "daily-refresh"
                          ? "Daily revenue refresh"
                          : `${model.agentName} · Version ${origin === "agent-review" && model.runStarted ? "2" : "1"}`}
                      </Link>,
                    ],
                  ]}
                />
              </>
            ) : route.tab === "retention" ? (
              <>
                <h2>Retention</h2>
                <Notice title="Retained in workspace storage" tone="success">
                  The sample receipt confirms retention. This state is independent of the run's
                  success label.
                </Notice>
                <Details
                  values={[
                    ["Retention policy", "Reports · 90 days"],
                    [
                      "Retained at",
                      origin === "previous-summary" ? "28 Oct 2026, 16:30" : "Just now (sample)",
                    ],
                    ["Deletion", "Not requested"],
                  ]}
                />
              </>
            ) : (
              <>
                <div className="section-title">
                  <h2>Report preview</h2>
                  <Button onPress={() => setRevealed((value) => !value)}>
                    {revealed ? "Hide content" : "Read content"}
                  </Button>
                </div>
                {revealed ? (
                  <article className="report">
                    <p className="eyebrow">Northstar Retail / Sample report</p>
                    <h2>Revenue by region</h2>
                    <p>
                      Europe led regional net revenue at {money(284350)}. North America contributed{" "}
                      {money(246800)}. Values are fictional and provided for design review.
                    </p>
                    <RevenueChart />
                    <RevenueTable />
                  </article>
                ) : (
                  <Notice title="Restricted content">
                    The metadata is available. Read the content explicitly to inspect the sample
                    report.
                  </Notice>
                )}
              </>
            )}
          </section>
          <aside className="detail-aside">
            <h3>Artifact details</h3>
            <Details
              values={[
                ["Type", "Markdown report"],
                ["Size", "4.2 KB"],
                ["Sensitivity", "Restricted"],
                [
                  "Producing run",
                  <Link key="r" to={`/runs/${origin}`}>
                    Run {runNumber} · {originName}
                  </Link>,
                ],
                [
                  "Source",
                  <Link key="s" to="/catalog/orders">
                    orders
                  </Link>,
                ],
              ]}
            />
            <Button variant="quiet" onPress={() => inspect("metadata")}>
              Integrity and identifiers
            </Button>
          </aside>
        </div>
      </>
    );
  return (
    <>
      <Header
        eyebrow="Results and evidence"
        title="Artifacts"
        description="Find retained outputs and follow them back to their source."
      />
      <div className="content-pad">
        <Filter value={query} onChange={setQuery} placeholder="Search artifacts" />
        <Table
          label="Artifacts"
          headers={["Name", "Type", "Producing run", "Retention", "Created"]}
          rows={[
            {
              id: "revenue-summary",
              run: "previous-summary",
              name: "October revenue summary",
              source: "Revenue analyst / Run 42",
              created: "Yesterday, 16:30",
            },
            ...(model.run === "succeeded"
              ? [
                  {
                    id: "agent-summary",
                    run: "agent-review",
                    name: "October revenue summary · latest agent run",
                    source: `${model.agentName} / Run 43`,
                    created: "Just now",
                  },
                ]
              : []),
            ...(model.jobRepaired
              ? [
                  {
                    id: "job-summary",
                    run: "daily-refresh",
                    name: "Daily revenue summary · repaired run",
                    source: "Daily revenue refresh / Run 184",
                    created: "Just now",
                  },
                ]
              : []),
          ]
            .filter((item) => item.name.toLowerCase().includes(query.toLowerCase()))
            .map((item) => ({
              key: item.id,
              cells: [
                <ResourceName
                  key="name"
                  name={item.name}
                  kind="Artifact"
                  to={`/artifacts/${item.id}`}
                />,
                "Markdown report",
                <Link key="origin" to={`/runs/${item.run}`}>
                  {item.source}
                </Link>,
                <Badge key="retention" tone="success">
                  Retained
                </Badge>,
                item.created,
              ],
            }))}
        />
      </div>
    </>
  );
}
export function Admin({ route, query, setQuery, inspect }: PageProps) {
  return (
    <>
      <Header
        eyebrow="Administration"
        title="Workspace administration"
        description="Inspect access, runtime readiness, and recorded decisions."
      />
      <Tabs
        base="/admin"
        value={route.tab}
        items={["Access", "Policies", "Environments", "Capacity", "Audit", "Installation"]}
      />
      <div className="content-pad">
        {route.tab === "policies" ? (
          <>
            <h2>Access policies</h2>
            <Table
              label="Policy summaries"
              headers={["Policy", "Scope", "Effect", "Details"]}
              rows={[
                {
                  key: "commerce",
                  cells: [
                    "Commerce analyst access",
                    "Retail operations / commerce",
                    "Read data",
                    <Button key="details" variant="quiet" onPress={() => inspect("policy")}>
                      Inspect policy
                    </Button>,
                  ],
                },
                {
                  key: "finance",
                  cells: [
                    "Finance reviewer access",
                    "finance catalog",
                    "Restricted to Finance reviewers",
                    <Button key="details" variant="quiet" onPress={() => inspect("access")}>
                      Inspect restriction
                    </Button>,
                  ],
                },
              ]}
            />
            <p className="muted">
              Read-only sample summaries. Policy authoring requires a reviewed public contract.
            </p>
          </>
        ) : route.tab === "installation" ? (
          <>
            <h2>Installation settings</h2>
            <Details
              values={[
                ["Installation", "Northstar Retail · Fictional"],
                ["Available modules", "Workspace, Catalog, SQL, Agents, Jobs, Artifacts"],
                ["Identity", "Sample identity only"],
                ["Deployment profile", "Design preview; no production readiness claim"],
              ]}
            />
            <Notice title="Operator configuration is separate">
              Identity providers, infrastructure and runtime credentials are not configured through
              this preview.
            </Notice>
          </>
        ) : route.tab === "environments" ? (
          <>
            <h2>Execution environments</h2>
            <Table
              label="Environments"
              headers={["Name", "Purpose", "Readiness", "Action"]}
              rows={[
                {
                  key: "sql",
                  cells: [
                    "Shared SQL",
                    "Interactive analysis",
                    <Badge key="s" tone="success">
                      Ready
                    </Badge>,
                    <Button key="a" variant="quiet" onPress={() => inspect("sql-runtime")}>
                      Inspect readiness
                    </Button>,
                  ],
                },
                {
                  key: "agent",
                  cells: [
                    "Legacy agent environment",
                    "Agent execution",
                    <Badge key="s" tone="warning">
                      Degraded
                    </Badge>,
                    <Button key="a" variant="quiet" onPress={() => inspect("runtime")}>
                      Inspect readiness
                    </Button>,
                  ],
                },
              ]}
            />
          </>
        ) : route.tab === "capacity" ? (
          <>
            <h2>Capacity evidence</h2>
            <Notice title="No capacity evidence in this prototype">
              A deployment must report an observed, certified envelope before this view can show
              capacity or utilization. No synthetic health percentage is displayed.
            </Notice>
            <Link to="/admin?tab=environments">Inspect environment readiness</Link>
          </>
        ) : route.tab === "audit" ? (
          <>
            <h2>Recent decisions</h2>
            <Table
              label="Administration audit"
              headers={["Time", "Action", "Actor", "Decision", "Resource"]}
              rows={[
                {
                  key: "1",
                  cells: [
                    "09:42",
                    "Read table",
                    "Alex Morgan",
                    "Allowed",
                    "retail.commerce.orders",
                  ],
                },
                {
                  key: "2",
                  cells: [
                    "09:41",
                    "Read catalog",
                    "Alex Morgan",
                    "Denied",
                    <Link key="f" to="/catalog/finance">
                      finance
                    </Link>,
                  ],
                },
              ]}
            />
          </>
        ) : (
          <>
            <Filter value={query} onChange={setQuery} placeholder="Search principals" />
            <Table
              label="Workspace principals"
              headers={["Principal", "Role", "Scope", "Action"]}
              rows={[
                {
                  key: "alex",
                  cells: [
                    "Alex Morgan",
                    "Analyst & builder",
                    "Retail operations",
                    <Button key="a" variant="quiet" onPress={() => inspect("access")}>
                      Inspect access
                    </Button>,
                  ],
                },
                {
                  key: "retail",
                  cells: [
                    "Retail data team",
                    "Publisher",
                    "Commerce catalog",
                    <Button key="a" variant="quiet" onPress={() => inspect("policy")}>
                      Inspect policy
                    </Button>,
                  ],
                },
              ].filter((row) => String(row.cells[0]).toLowerCase().includes(query.toLowerCase()))}
            />
            <div className="related">
              <h3>Understand a restriction</h3>
              <Link to="/catalog/finance">Finance catalog · Access denied</Link>
            </div>
          </>
        )}
      </div>
    </>
  );
}
