import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { expect, userEvent, within } from "storybook/test";
import { Tabs } from "../src/Tabs";
import { TextField } from "../src/TextField";

function Draft() {
  const [value, setValue] = useState("");
  return <TextField label="Draft name" value={value} onChange={setValue} />;
}
function Example() {
  const [selected, setSelected] = useState("build");
  return (
    <Tabs
      label="Service views"
      selectedId={selected}
      onSelectionChange={setSelected}
      items={[
        { id: "overview", label: "Overview", content: <p>A service overview.</p> },
        { id: "build", label: "Build", content: <Draft /> },
        { id: "versions", label: "Versions", content: <p>No versions yet.</p> },
        { id: "restricted", label: "Restricted", content: <p>Unavailable</p>, isDisabled: true },
      ]}
    />
  );
}
const meta = { title: "Primitives/Tabs", component: Example, tags: ["autodocs"] } satisfies Meta<
  typeof Example
>;
export default meta;
type Story = StoryObj<typeof meta>;
export const RetainedDraft: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(canvas.getByRole("textbox", { name: "Draft name" }), "Retained analysis");
    await userEvent.click(canvas.getByRole("tab", { name: "Overview" }));
    await expect(canvas.queryByRole("textbox", { name: "Draft name" })).not.toBeInTheDocument();
    await userEvent.click(canvas.getByRole("tab", { name: "Build" }));
    await expect(canvas.getByRole("textbox", { name: "Draft name" })).toHaveValue(
      "Retained analysis",
    );
  },
};
export const KeyboardNavigation: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("tab", { name: "Build" }));
    await userEvent.keyboard("{ArrowRight}");
    await expect(canvas.getByRole("tab", { name: "Versions" })).toHaveFocus();
    await expect(canvas.getByRole("tab", { name: "Build" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    await userEvent.keyboard("{Enter}");
    await expect(canvas.getByRole("tab", { name: "Versions" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    await expect(canvas.getByRole("tab", { name: "Restricted" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
  },
};
