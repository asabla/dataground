import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { expect, fn, userEvent, within } from "storybook/test";
import { Button } from "../src/Button";
import { DataTable, type DataTableSort } from "../src/DataTable";

const items = [
  {
    id: "review",
    name: "Change review",
    description: "Review a source change",
    unavailable: false,
  },
  {
    id: "analysis",
    name: "Regional revenue analysis and source reconciliation",
    description: "A long resource name remains readable",
    unavailable: false,
  },
  {
    id: "restricted",
    name: "Restricted resource",
    description: "Read unavailable",
    unavailable: true,
  },
];

function TableExample({
  onOpen,
  empty = false,
  loading = false,
}: {
  onOpen: (id: string) => void;
  empty?: boolean;
  loading?: boolean;
}) {
  const [sort, setSort] = useState<DataTableSort>({ column: "name", direction: "ascending" });
  const ordered = [...items].sort(
    (a, b) => (sort.direction === "ascending" ? 1 : -1) * a.name.localeCompare(b.name),
  );
  return (
    <DataTable
      label="Service examples"
      items={empty ? [] : ordered}
      getRowId={(item) => item.id}
      getRowLabel={(item) => item.name}
      sort={sort}
      onSortChange={setSort}
      isLoading={loading}
      emptyMessage="No services match this filter."
      columns={[
        {
          id: "name",
          title: "Name",
          isRowHeader: true,
          allowsSorting: true,
          render: (item) => item.name,
        },
        { id: "description", title: "Description", render: (item) => item.description },
        {
          id: "action",
          title: "Action",
          render: (item) => (
            <Button
              aria-label={`Open ${item.name}`}
              isDisabled={item.unavailable || loading}
              onPress={() => onOpen(item.id)}
            >
              Open
            </Button>
          ),
        },
      ]}
    />
  );
}

const meta = {
  title: "Primitives/DataTable",
  component: TableExample,
  args: { onOpen: fn() },
  tags: ["autodocs"],
  parameters: {
    docs: {
      description: {
        component:
          "A controlled resource grid. Consumers own sorting, pagination, loading, and authority. Row actions are explicit controls. Arrow keys move through cells; with a cell focused, End reaches the last cell and its action. Horizontal overflow retains the full table at narrow widths.",
      },
    },
  },
} satisfies Meta<typeof TableExample>;
export default meta;
type Story = StoryObj<typeof meta>;

export const ResourceActions: Story = {
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("grid", { name: "Service examples" })).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Open Restricted resource" })).toBeDisabled();
    await userEvent.click(canvas.getByRole("button", { name: "Open Change review" }));
    await expect(args.onOpen).toHaveBeenCalledWith("review");
  },
};
export const KeyboardSorting: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const header = canvas.getByRole("columnheader", { name: "Name" });
    await userEvent.click(header);
    await expect(header).toHaveAttribute("aria-sort", "descending");
    await userEvent.keyboard("{Enter}");
    await expect(header).toHaveAttribute("aria-sort", "ascending");
    await userEvent.keyboard("{ArrowDown}");
    await expect(canvas.getByRole("rowheader", { name: "Change review" })).toHaveFocus();
  },
};
export const Empty: Story = { args: { empty: true } };
export const Loading: Story = { args: { empty: true, loading: true } };

function RefreshExample({ onOpen }: { onOpen: (id: string) => void }) {
  const [loading, setLoading] = useState(false);
  return (
    <>
      <Button onPress={() => setLoading(true)}>Refresh resources</Button>
      <TableExample onOpen={onOpen} loading={loading} />
    </>
  );
}

export const RefreshDisablesStaleActions: Story = {
  render: (args) => <RefreshExample onOpen={args.onOpen} />,
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("button", { name: "Open Change review" })).toBeEnabled();
    await userEvent.click(canvas.getByRole("button", { name: "Refresh resources" }));
    await expect(canvas.getByRole("button", { name: "Open Change review" })).toBeDisabled();
    await expect(
      canvas.getByRole("button", {
        name: "Open Regional revenue analysis and source reconciliation",
      }),
    ).toBeDisabled();
    await expect(args.onOpen).not.toHaveBeenCalled();
  },
};

export const KeyboardRowAction: Story = {
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("rowheader", { name: "Change review" }));
    await userEvent.keyboard("{End}");
    await expect(canvas.getByRole("button", { name: "Open Change review" })).toHaveFocus();
    await userEvent.keyboard("{Enter}");
    await expect(args.onOpen).toHaveBeenCalledWith("review");
  },
};
