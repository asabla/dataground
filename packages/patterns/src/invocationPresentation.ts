import type { StatusTone } from "@dataground/ui";
import type { InvocationOperationResource } from "./InvocationStatus";

export interface StatePresentation {
  label: string;
  tone: StatusTone;
}

export const statePresentations: Record<string, StatePresentation> = {
  accepted: { label: "Invocation accepted", tone: "active" },
  cancelled: { label: "Invocation cancelled", tone: "warning" },
  cancelling: { label: "Cancellation in progress", tone: "warning" },
  failed: { label: "Invocation failed", tone: "critical" },
  running: { label: "Invocation running", tone: "active" },
  succeeded: { label: "Invocation succeeded", tone: "success" },
  unknown: { label: "Invocation state unknown", tone: "neutral" },
  waiting: { label: "Invocation waiting", tone: "waiting" },
};

export function displayText(value: string, maximum = 255): string {
  const normalized = Array.from(value, (character) => {
    const codePoint = character.codePointAt(0) ?? 0;
    return codePoint <= 8 ||
      codePoint === 11 ||
      codePoint === 12 ||
      (codePoint >= 14 && codePoint <= 31) ||
      (codePoint >= 127 && codePoint <= 159)
      ? "�"
      : character;
  })
    .join("")
    .replaceAll(/\s+/gu, " ")
    .trim();
  return normalized.length > maximum ? `${normalized.slice(0, maximum)}…` : normalized;
}

export function statePresentation(state: string): StatePresentation {
  return (
    statePresentations[state] ?? {
      label: `Unknown state: ${displayText(state, 64)}`,
      tone: "neutral",
    }
  );
}

export function operationPresentation(operation: InvocationOperationResource): StatePresentation {
  if (operation.observedState === "failed") {
    return { label: "Operation failed", tone: "critical" };
  }
  if (
    operation.desiredState === operation.observedState &&
    (operation.observedState === "succeeded" || operation.observedState === "cancelled")
  ) {
    return { label: "Desired state observed", tone: "success" };
  }
  if (operation.desiredState === "unknown" || operation.observedState === "unknown") {
    return { label: "Operation state unknown", tone: "neutral" };
  }
  return operation.desiredState === operation.observedState
    ? { label: "Operation state aligned", tone: "neutral" }
    : { label: "Reconciliation in progress", tone: "active" };
}

export function formatInteger(value: number): string {
  return new Intl.NumberFormat("en-US").format(value);
}
