import { Button, StatusBadge } from "@dataground/ui";
import { isValidElement, type ReactNode, useEffect, useId, useRef, useState } from "react";

export { Button } from "@dataground/ui";

const paths: Record<string, string> = {
  home: "M3 10 12 3l9 7M5 9v12h5v-7h4v7h5V9",
  workspace: "M3 6h7l2 3h9v11H3zM3 6V4h7l2 2",
  catalog: "M4 5c0-3 16-3 16 0s-16 3-16 0v14c0 3 16 3 16 0V5M4 12c0 3 16 3 16 0",
  sql: "m8 6-6 6 6 6m8-12 6 6-6 6m-3-14-2 16",
  agents: "M12 2v4M5 7h14v13H5zM8 11h1m6 0h1M8 16h8M2 10v6m20-6v6",
  jobs: "M3 3h6v6H3zM15 15h6v6h-6zM3 15h6v6H3zM6 9v6m3 3h6M9 6h9v9",
  artifacts: "M5 2h9l5 5v15H5zM14 2v6h5M8 12h8m-8 4h8",
  admin: "M12 2 3 6v6c0 5 9 10 9 10s9-5 9-10V6zM8 12l3 3 5-6",
  search: "M20 20l-5-5M17 10a7 7 0 1 1-14 0 7 7 0 0 1 14 0",
  plus: "M12 4v16M4 12h16",
  arrow: "M4 12h16m-6-6 6 6-6 6",
  chevron: "m9 5 7 7-7 7",
  close: "m6 6 12 12M6 18 18 6",
  clock: "M12 7v5l3 2M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0",
  notebook: "M5 3h15v18H5zM2 7h6m-6 5h6m-6 5h6m3-10h6m-6 5h6",
  chart: "M3 3v18h18M7 16v-5m5 5V7m5 9V4",
};
export function Icon({ name, size = 18 }: { name: string; size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d={paths[name] || paths.artifacts} />
    </svg>
  );
}
export function Link({
  to,
  children,
  className = "",
  current,
}: {
  to: string;
  children: ReactNode;
  className?: string;
  current?: boolean;
}) {
  return (
    <a className={className} href={`#${to}`} aria-current={current ? "page" : undefined}>
      {children}
    </a>
  );
}
export function Badge({
  children,
  tone = "neutral",
}: {
  children: ReactNode;
  tone?: "neutral" | "success" | "warning" | "critical" | "info";
}) {
  return <StatusBadge tone={tone === "info" ? "active" : tone}>{children}</StatusBadge>;
}
export function Header({
  eyebrow,
  title,
  description,
  actions,
}: {
  eyebrow?: ReactNode;
  title: string;
  description?: string;
  actions?: ReactNode;
}) {
  return (
    <header className="page-header">
      <div>
        {eyebrow && <div className="breadcrumbs">{eyebrow}</div>}
        <h1 tabIndex={-1}>{title}</h1>
        {description && <p>{description}</p>}
      </div>
      <div className="actions">{actions}</div>
    </header>
  );
}
export function Tabs({
  base,
  value,
  items,
}: {
  base: string;
  value: string;
  items: readonly string[];
}) {
  return (
    <nav className="tabs" aria-label="Resource views">
      {items.map((label, i) => {
        const key = i === 0 ? "overview" : label.toLowerCase().replaceAll(" ", "-");
        return (
          <Link
            key={key}
            to={`${base}?tab=${key}`}
            className={key === value ? "active" : ""}
            current={key === value}
          >
            {label}
          </Link>
        );
      })}
    </nav>
  );
}
function cellLabel(cell: ReactNode): string {
  if (typeof cell === "string" || typeof cell === "number") return String(cell);
  if (Array.isArray(cell)) return cell.map(cellLabel).join(" ");
  if (isValidElement<{ children?: ReactNode; name?: string }>(cell))
    return cell.props.name || cellLabel(cell.props.children);
  return "";
}
export function Table({
  label,
  headers,
  rows,
  empty = "No matching resources. Clear the search or choose another filter.",
}: {
  label: string;
  headers: string[];
  rows: { key: string; cells: ReactNode[] }[];
  empty?: string;
}) {
  const [sort, setSort] = useState<"none" | "ascending" | "descending">("none");
  const ordered =
    sort === "none"
      ? rows
      : [...rows].sort(
          (a, b) =>
            (sort === "ascending" ? 1 : -1) *
            cellLabel(a.cells[0]).localeCompare(cellLabel(b.cells[0]), "en", { numeric: true }),
        );
  return (
    // biome-ignore lint/a11y/noNoninteractiveTabindex: The scroll region must support keyboard scrolling at narrow widths.
    <section className="table-scroll" aria-label={label} tabIndex={0}>
      <table>
        <thead>
          <tr>
            {headers.map((header) => (
              <th key={header} scope="col" aria-sort={header === headers[0] ? sort : undefined}>
                {header === headers[0] ? (
                  <button
                    type="button"
                    className="sort-heading"
                    onClick={() => setSort(sort === "ascending" ? "descending" : "ascending")}
                    aria-label={`Sort by ${header}`}
                  >
                    {header}
                    <span aria-hidden="true">
                      {sort === "ascending" ? " ↑" : sort === "descending" ? " ↓" : " ↕"}
                    </span>
                  </button>
                ) : (
                  header
                )}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {ordered.map((row) => (
            <tr key={row.key}>
              {row.cells.map((cell, i) => (
                <td key={headers[i]}>{cell}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      {rows.length === 0 && (
        <div className="empty">
          <Icon name="search" size={28} />
          <h3>No results</h3>
          <p>{empty}</p>
        </div>
      )}
    </section>
  );
}
export function Filter({
  value,
  onChange,
  placeholder = "Search resources",
  children,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  children?: ReactNode;
}) {
  return (
    <div className="filterbar">
      <label className="search-field">
        <Icon name="search" />
        <input
          aria-label={placeholder}
          type="search"
          placeholder={placeholder}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      </label>
      {children}
    </div>
  );
}
export function Notice({
  title,
  children,
  tone = "info",
}: {
  title: string;
  children?: ReactNode;
  tone?: "info" | "warning" | "critical" | "success";
}) {
  return (
    <div className={`notice notice--${tone}`} role={tone === "critical" ? "alert" : "status"}>
      <strong>{title}</strong>
      {children && <div>{children}</div>}
    </div>
  );
}
export function Empty({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="empty">
      <Icon name="workspace" size={32} />
      <h2>{title}</h2>
      <p>{children}</p>
    </div>
  );
}
export function Panel({
  title,
  children,
  onClose,
  drawer = false,
}: {
  title: string;
  children: ReactNode;
  onClose: () => void;
  drawer?: boolean;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  const close = useRef(onClose);
  close.current = onClose;
  useEffect(() => {
    const dialog = ref.current;
    const origin = document.activeElement;
    dialog?.showModal();
    return () => {
      dialog?.close();
      if (origin instanceof HTMLElement && origin.isConnected) origin.focus();
    };
  }, []);
  return (
    <dialog
      ref={ref}
      className={drawer ? "drawer" : "modal"}
      aria-labelledby={titleId}
      onCancel={(event) => {
        event.preventDefault();
        close.current();
      }}
    >
      <div className="panel-heading">
        <h2 id={titleId}>{title}</h2>
        <Button variant="quiet" aria-label={`Close ${title}`} onPress={onClose}>
          <Icon name="close" />
        </Button>
      </div>
      <div className="panel-body">{children}</div>
    </dialog>
  );
}
export function Field({
  label,
  value,
  onChange,
  multiline = false,
  disabled = false,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  multiline?: boolean;
  disabled?: boolean;
}) {
  const id = useId();
  return (
    <label className="field" htmlFor={id}>
      <span>{label}</span>
      {multiline ? (
        <textarea
          value={value}
          onChange={(e) => onChange(e.target.value)}
          disabled={disabled}
          rows={5}
        />
      ) : (
        <input
          id={id}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          disabled={disabled}
        />
      )}
    </label>
  );
}
export function Details({ values }: { values: [string, ReactNode][] }) {
  return (
    <dl className="details">
      {values.map(([label, value]) => (
        <div key={label}>
          <dt>{label}</dt>
          <dd>{value}</dd>
        </div>
      ))}
    </dl>
  );
}
