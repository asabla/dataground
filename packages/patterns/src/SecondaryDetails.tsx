import type { ReactNode } from "react";

// Errors, sensitivity notices and required decisions stay outside this disclosure.
// Card consumers retain their existing presentation during migration.
export function SecondaryDetails({
  compact,
  children,
  label = "Resource details",
}: {
  compact: boolean;
  children: ReactNode;
  label?: string;
}) {
  return compact ? (
    <details className="dg-secondary-details">
      <summary>{label}</summary>
      {children}
    </details>
  ) : (
    children
  );
}
