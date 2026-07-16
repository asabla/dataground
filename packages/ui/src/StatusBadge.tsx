import type { HTMLAttributes, ReactNode } from "react";

export type StatusTone = "neutral" | "active" | "waiting" | "success" | "warning" | "critical";

export interface StatusBadgeProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  children: ReactNode;
  tone?: StatusTone;
}

export function StatusBadge({ children, className, tone = "neutral", ...props }: StatusBadgeProps) {
  return (
    <span
      {...props}
      className={["dg-status-badge", className].filter(Boolean).join(" ")}
      data-tone={tone}
    >
      <span aria-hidden="true" className="dg-status-badge__marker" />
      <span>{children}</span>
    </span>
  );
}
