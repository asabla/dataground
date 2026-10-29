import type { ReactNode } from "react";
import { Cell, Column, Row, Table, TableBody, TableHeader } from "react-aria-components";

export interface DataTableSort {
  column: string;
  direction: "ascending" | "descending";
}

export interface DataTableColumn<T> {
  id: string;
  title: string;
  isRowHeader?: boolean;
  allowsSorting?: boolean;
  render: (item: T) => ReactNode;
}

export interface DataTableProps<T extends object> {
  label: string;
  columns: readonly DataTableColumn<T>[];
  items: readonly T[];
  getRowId: (item: T) => string;
  getRowLabel: (item: T) => string;
  emptyMessage?: string;
  isLoading?: boolean;
  sort?: DataTableSort;
  onSortChange?: (sort: DataTableSort) => void;
}

// Consumers own ordering, pagination and authority. A row never implies an
// action or selection; each consequential action remains an explicit control.
export function DataTable<T extends object>({
  label,
  columns,
  items,
  getRowId,
  getRowLabel,
  emptyMessage = "No resources to display.",
  isLoading = false,
  sort,
  onSortChange,
}: DataTableProps<T>) {
  return (
    <div className="dg-data-table-scroll" aria-busy={isLoading}>
      <Table
        aria-label={label}
        className="dg-data-table"
        selectionMode="none"
        sortDescriptor={sort}
        onSortChange={
          onSortChange
            ? (next) => {
                if (typeof next.column === "string" && next.direction) {
                  onSortChange({ column: next.column, direction: next.direction });
                }
              }
            : undefined
        }
      >
        <TableHeader columns={columns}>
          {(column) => (
            <Column
              id={column.id}
              isRowHeader={column.isRowHeader}
              allowsSorting={column.allowsSorting && onSortChange !== undefined}
              className="dg-data-table__column"
            >
              {({ sortDirection }) => (
                <>
                  {column.title}
                  {column.allowsSorting && onSortChange && (
                    <span aria-hidden="true" className="dg-data-table__sort">
                      {sortDirection === "ascending"
                        ? "↑"
                        : sortDirection === "descending"
                          ? "↓"
                          : "↕"}
                    </span>
                  )}
                </>
              )}
            </Column>
          )}
        </TableHeader>
        <TableBody
          items={items}
          dependencies={[columns]}
          renderEmptyState={() => (
            <span className="dg-data-table__empty">
              {isLoading ? "Loading resources…" : emptyMessage}
            </span>
          )}
        >
          {(item) => (
            <Row id={getRowId(item)} textValue={getRowLabel(item)} columns={columns}>
              {(column) => <Cell className="dg-data-table__cell">{column.render(item)}</Cell>}
            </Row>
          )}
        </TableBody>
      </Table>
    </div>
  );
}
