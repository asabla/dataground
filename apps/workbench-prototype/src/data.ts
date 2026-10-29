// All names, records, timings and relationships in this app are fictional review fixtures.
export const exampleQuery = `SELECT
  region,
  COUNT(*) AS orders,
  ROUND(SUM(net_revenue), 2) AS revenue
FROM retail.commerce.orders
WHERE ordered_at >= DATE '2026-10-01'
GROUP BY region
ORDER BY revenue DESC;`;

export const notebookCode = `orders = catalog.read("retail.commerce.orders")
revenue = orders.groupby("region")["net_revenue"].sum()
display(revenue.sort_values(ascending=False))`;

export const revenue = [
  { region: "Europe", orders: 8420, revenue: 284350 },
  { region: "North America", orders: 6980, revenue: 246800 },
  { region: "Asia Pacific", orders: 4260, revenue: 168240 },
  { region: "Latin America", orders: 1820, revenue: 62480 },
];

export const datasets = [
  {
    id: "orders",
    name: "orders",
    description: "Completed and pending customer orders",
    rows: "21,480",
    updated: "Today, 09:42",
    type: "Table",
  },
  {
    id: "customers",
    name: "customers",
    description: "Customer profiles and regional segments",
    rows: "12,804",
    updated: "Today, 09:40",
    type: "Table",
  },
  {
    id: "order-items",
    name: "order_items",
    description: "Product quantities and line-level revenue",
    rows: "64,912",
    updated: "Today, 09:42",
    type: "Table",
  },
  {
    id: "inventory",
    name: "inventory_availability_by_distribution_center",
    description: "Daily available stock across fulfillment locations",
    rows: "3,280",
    updated: "Today, 08:00",
    type: "View",
  },
];

export const columns = [
  ["order_id", "STRING", "Unique order reference", "No"],
  ["ordered_at", "TIMESTAMP", "Time the customer placed the order", "No"],
  ["customer_id", "STRING", "Reference to the customer profile", "No"],
  ["region", "STRING", "Sales region", "No"],
  ["net_revenue", "DECIMAL(12,2)", "Revenue after discounts, in USD", "No"],
  ["currency", "STRING", "Source transaction currency", "No"],
  ["status", "STRING", "Current order state", "No"],
];

export const orders = [
  ["ORD-10482", "29 Oct, 09:41", "CUS-2041", "Europe", "$128.00", "EUR", "Completed"],
  ["ORD-10481", "29 Oct, 09:40", "CUS-1830", "North America", "$84.50", "USD", "Completed"],
  ["ORD-10480", "29 Oct, 09:39", "CUS-0912", "Asia Pacific", "$246.00", "JPY", "Completed"],
  ["ORD-10479", "29 Oct, 09:38", "CUS-1428", "Europe", "$67.20", "SEK", "Pending"],
  ["ORD-10478", "29 Oct, 09:37", "CUS-1156", "Latin America", "$113.40", "BRL", "Completed"],
];

export const resources = [
  {
    name: "orders",
    kind: "Table",
    path: "retail / commerce",
    to: "/catalog/orders",
    updated: "Today, 09:42",
  },
  {
    name: "Revenue by region",
    kind: "SQL query",
    path: "Sales analytics",
    to: "/sql/revenue",
    updated: "Today, 09:30",
  },
  {
    name: "October revenue analysis",
    kind: "Notebook",
    path: "Sales analytics",
    to: "/workspace/revenue-notebook",
    updated: "Today, 09:15",
  },
  {
    name: "Revenue analyst",
    kind: "Agent service",
    path: "Agents",
    to: "/agents/revenue",
    updated: "Yesterday, 16:24",
  },
  {
    name: "Daily revenue refresh",
    kind: "Job",
    path: "Jobs & Runs",
    to: "/jobs/revenue-refresh",
    updated: "Today, 09:00",
  },
  {
    name: "October revenue summary",
    kind: "Artifact",
    path: "Revenue analyst / Run 42",
    to: "/artifacts/revenue-summary",
    updated: "Yesterday, 16:30",
  },
];

export const sections = [
  "Home",
  "Workspace",
  "Catalog",
  "SQL",
  "Agents",
  "Jobs & Runs",
  "Artifacts",
  "Administration",
] as const;
export const sectionPaths = [
  "/home",
  "/workspace",
  "/catalog",
  "/sql/revenue",
  "/agents",
  "/jobs",
  "/artifacts",
  "/admin",
];
export const notes: Record<string, string[]> = {
  home: [
    "Start from recent work and required decisions, not platform counters.",
    "The same sample resources connect every journey.",
    "Home summaries need authorized cross-resource read models before production.",
  ],
  workspace: [
    "Files stay in context while an editor is open.",
    "Notebook code and unsaved state remain in memory across navigation.",
    "The prototype has no notebook storage or kernel connection.",
  ],
  catalog: [
    "Browse hierarchy on the left, compare records in the center.",
    "Schema, sample data, lineage and access are separate resource views.",
    "All samples and relationships are fixtures; catalog APIs are not implemented.",
  ],
  sql: [
    "Keep query, catalog context and results together.",
    "Table and chart are two views of the same result.",
    "Only the provided example executes; this app is not a SQL engine.",
  ],
  agents: [
    "Build, version publication and route assignment are separate actions.",
    "Configuration does not occupy the daily run-monitoring view.",
    "Persistent assistants retain distinct memory, schedule and channel surfaces.",
  ],
  jobs: [
    "Use a table for comparison and a graph only for known task dependencies.",
    "A failed run leads directly to the failed task and an explicit recovery action.",
    "The sample repair changes local fixture state only.",
  ],
  runs: [
    "Put the outcome or required decision before implementation metadata.",
    "Activity, results, files and audit have different reading tasks.",
    "Question answers and approvals remain distinct steps; uncertain results cannot be resubmitted.",
  ],
  artifacts: [
    "Show usable output first, with links back to its run and source.",
    "Sensitive content requires an explicit reveal action.",
    "Retention is shown as an observed fixture state, not inferred from a successful run.",
  ],
  admin: [
    "Separate administration from analysis and authoring.",
    "Explain denied access and degraded runtime readiness without exposing credentials or endpoints.",
    "The prototype grants no real permissions and performs no runtime repair.",
  ],
};

export const money = (value: number) =>
  new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
    maximumFractionDigits: 0,
  }).format(value);
