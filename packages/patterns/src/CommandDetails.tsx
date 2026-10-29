import type { ReactNode } from "react";

// Command errors and required decisions stay outside this secondary disclosure.
// Card consumers retain their existing presentation during migration.
export function CommandDetails({
  compact,
  children,
  label = "Resource details",
}: {
  compact: boolean;
  children: ReactNode;
  label?: string;
}) {
  return compact ? (
    <details className="dg-command-details">
      <summary>{label}</summary>
      {children}
    </details>
  ) : (
    children
  );
}
