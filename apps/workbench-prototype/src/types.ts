import type { Dispatch } from "react";
import type { Action, Model, Route, Scenario } from "./model";
export type Inspector =
  | "guide"
  | "search"
  | "create"
  | "publish"
  | "route"
  | "invoke"
  | "approval"
  | "task"
  | "policy"
  | "access"
  | "runtime"
  | "sql-runtime"
  | "metadata"
  | "catalog"
  | "new-agent"
  | "discard"
  | "cancel"
  | null;
export interface PageProps {
  route: Route;
  model: Model;
  dispatch: Dispatch<Action>;
  scenario: Scenario;
  inspect: (panel: Inspector) => void;
  query: string;
  setQuery: (value: string) => void;
}
