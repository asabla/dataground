export type RunTab = "activity" | "requests" | "output" | "files" | "audit";

export function isRunTab(value: string): value is RunTab {
  return ["activity", "requests", "output", "files", "audit"].includes(value);
}

export interface RunLocation {
  invocationId?: string;
  tab?: RunTab;
}
