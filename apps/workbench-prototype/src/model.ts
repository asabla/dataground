import { exampleQuery, notebookCode } from "./data";

export type Scenario =
  | "normal"
  | "loading"
  | "empty"
  | "denied"
  | "offline"
  | "failed"
  | "expired"
  | "conflict"
  | "uncertain"
  | "cancelling";
export type RunState =
  | "question"
  | "approval"
  | "succeeded"
  | "denied"
  | "cancelling"
  | "cancelled";
export interface Model {
  query: string;
  queryStatus: "idle" | "running" | "complete" | "unsupported" | "cancelled";
  queryRuns: number;
  savedQuery: boolean;
  notebook: string;
  notebookStatus: "idle" | "running" | "complete" | "unsupported";
  notebookSaved: boolean;
  agentName: string;
  purpose: string;
  publication: "draft" | "published" | "uncertain";
  routeAssigned: boolean;
  run: RunState;
  runStarted: boolean;
  answer: string;
  jobRepaired: boolean;
}
export const initialModel: Model = {
  query: exampleQuery,
  queryStatus: "idle",
  queryRuns: 0,
  savedQuery: false,
  notebook: notebookCode,
  notebookStatus: "idle",
  notebookSaved: true,
  agentName: "Revenue analyst",
  purpose:
    "Summarize monthly revenue by region. Ask which regions to include and request approval before publishing the summary.",
  publication: "draft",
  routeAssigned: false,
  run: "question",
  runStarted: false,
  answer: "",
  jobRepaired: false,
};
export type Action =
  | { type: "query-edit"; value: string }
  | { type: "query-run" | "query-complete" | "query-cancel" | "query-save" | "query-reset" }
  | { type: "notebook-edit"; value: string }
  | { type: "notebook-run" | "notebook-complete" | "notebook-save" }
  | { type: "agent-edit" | "agent-new"; name: string; purpose: string }
  | { type: "publish"; uncertain?: boolean }
  | { type: "publication-observe" | "assign-route" | "invoke" }
  | { type: "answer"; value: string; expired?: boolean }
  | { type: "approve" | "deny" | "cancel" | "cancel-complete" | "repair-job" | "reset" };

export function reducer(state: Model, action: Action): Model {
  switch (action.type) {
    case "reset":
      return { ...initialModel };
    case "query-edit":
      return { ...state, query: action.value, savedQuery: false, queryStatus: "idle" };
    case "query-reset":
      return { ...state, query: exampleQuery, queryStatus: "idle", savedQuery: false };
    case "query-run":
      return state.queryStatus === "running"
        ? state
        : {
            ...state,
            queryStatus: state.query.trim() === exampleQuery.trim() ? "running" : "unsupported",
          };
    case "query-complete":
      return state.queryStatus !== "running"
        ? state
        : { ...state, queryStatus: "complete", queryRuns: state.queryRuns + 1 };
    case "query-cancel":
      return state.queryStatus !== "running" ? state : { ...state, queryStatus: "cancelled" };
    case "query-save":
      return { ...state, savedQuery: true };
    case "notebook-edit":
      return { ...state, notebook: action.value, notebookSaved: false, notebookStatus: "idle" };
    case "notebook-run":
      return {
        ...state,
        notebookStatus: state.notebook.trim() === notebookCode.trim() ? "running" : "unsupported",
      };
    case "notebook-complete":
      return state.notebookStatus !== "running" ? state : { ...state, notebookStatus: "complete" };
    case "notebook-save":
      return { ...state, notebookSaved: true };
    case "agent-edit":
      return state.publication !== "draft"
        ? state
        : { ...state, agentName: action.name.trim() || "Untitled agent", purpose: action.purpose };
    case "agent-new":
      return {
        ...state,
        agentName: action.name.trim() || "Untitled agent",
        purpose: action.purpose,
        publication: "draft",
        routeAssigned: false,
        runStarted: false,
        run: "question",
        answer: "",
      };
    case "publish":
      return state.publication !== "draft"
        ? state
        : { ...state, publication: action.uncertain ? "uncertain" : "published" };
    case "publication-observe":
      return state.publication !== "uncertain" ? state : { ...state, publication: "published" };
    case "assign-route":
      return state.publication !== "published" ? state : { ...state, routeAssigned: true };
    case "invoke":
      return !state.routeAssigned || state.publication !== "published"
        ? state
        : { ...state, runStarted: true, run: "question", answer: "" };
    case "answer":
      return state.run !== "question" || action.expired || !action.value.trim()
        ? state
        : { ...state, answer: action.value, run: "approval" };
    case "approve":
      return state.run !== "approval" ? state : { ...state, run: "succeeded" };
    case "deny":
      return state.run !== "approval" ? state : { ...state, run: "denied" };
    case "cancel":
      return !["question", "approval"].includes(state.run)
        ? state
        : { ...state, run: "cancelling" };
    case "cancel-complete":
      return state.run !== "cancelling" ? state : { ...state, run: "cancelled" };
    case "repair-job":
      return { ...state, jobRepaired: true };
  }
}

export interface Route {
  area: string;
  id: string;
  tab: string;
}
const areas = new Set([
  "home",
  "workspace",
  "catalog",
  "sql",
  "agents",
  "jobs",
  "runs",
  "artifacts",
  "admin",
]);
export function parseRoute(hash: string): Route {
  const [path, search] = hash.replace(/^#/, "").split("?");
  const [, area = "home", id = ""] = (path || "/home").split("/");
  return {
    area: areas.has(area) ? area : "home",
    id,
    tab: new URLSearchParams(search).get("tab") || "overview",
  };
}
