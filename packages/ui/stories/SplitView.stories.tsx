import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { expect, userEvent, within } from "storybook/test";
import { Button } from "../src/Button";
import { SplitView } from "../src/SplitView";
import { TextField } from "../src/TextField";

function Example() {
  const [open, setOpen] = useState(true);
  const [draft, setDraft] = useState("");
  return (
    <>
      <Button onPress={() => setOpen((current) => !current)}>
        {open ? "Close inspection" : "Open inspection"}
      </Button>
      <SplitView
        primaryLabel="Run record"
        primary={<TextField label="Unfinished answer" value={draft} onChange={setDraft} />}
        secondary={
          open && (
            <section aria-label="Inspection">
              <h2>Selected request</h2>
              <p>Inspect the request without leaving the run.</p>
            </section>
          )
        }
      />
    </>
  );
}
const meta = {
  title: "Primitives/SplitView",
  component: Example,
  tags: ["autodocs"],
  decorators: [
    (Story) => (
      <div style={{ width: "64rem", maxWidth: "100%" }}>
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof Example>;
export default meta;
type Story = StoryObj<typeof meta>;
export const KeyboardAndRetention: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(
      canvas.getByRole("textbox", { name: "Unfinished answer" }),
      "Retained answer",
    );
    const separator = canvas.getByRole("separator", { name: "Run record" });
    await userEvent.click(separator);
    await userEvent.keyboard("{ArrowRight}");
    await expect(separator).toHaveAttribute("aria-valuenow", "62");
    await userEvent.keyboard("{Home}{ArrowLeft}");
    await expect(separator).toHaveAttribute("aria-valuenow", "40");
    await userEvent.keyboard("{End}{ArrowRight}");
    await expect(separator).toHaveAttribute("aria-valuenow", "70");
    await userEvent.click(canvas.getByRole("button", { name: "Close inspection" }));
    await expect(canvas.queryByRole("separator")).not.toBeInTheDocument();
    await expect(canvas.getByRole("textbox", { name: "Unfinished answer" })).toHaveValue(
      "Retained answer",
    );
    await userEvent.click(canvas.getByRole("button", { name: "Open inspection" }));
    await expect(canvas.getByRole("separator")).toHaveAttribute("aria-valuenow", "70");
    await expect(canvas.getByRole("textbox", { name: "Unfinished answer" })).toHaveValue(
      "Retained answer",
    );
  },
};
export const Narrow: Story = {
  decorators: [
    (Story) => (
      <div style={{ width: "24rem", maxWidth: "100%" }}>
        <Story />
      </div>
    ),
  ],
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("separator", { hidden: true })).not.toBeVisible();
    await expect(canvas.getByRole("textbox", { name: "Unfinished answer" })).toBeVisible();
    await expect(canvas.getByRole("heading", { name: "Selected request" })).toBeVisible();
  },
};
export const RightToLeft: Story = {
  decorators: [
    (Story) => (
      <div dir="rtl">
        <Story />
      </div>
    ),
  ],
  play: async ({ canvasElement }) => {
    const separator = within(canvasElement).getByRole("separator", { name: "Run record" });
    await userEvent.click(separator);
    await userEvent.keyboard("{ArrowLeft}");
    await expect(separator).toHaveAttribute("aria-valuenow", "62");
  },
};
export const PointerResize: Story = {
  play: async ({ canvasElement }) => {
    const separator = within(canvasElement).getByRole("separator", { name: "Run record" });
    const rect = separator.getBoundingClientRect();
    await userEvent.pointer([
      {
        keys: "[MouseLeft>]",
        target: separator,
        coords: {
          clientX: rect.left + 12,
          clientY: rect.top + 12,
          pageX: rect.left + 12,
          pageY: rect.top + 12,
        },
      },
      {
        target: separator,
        coords: {
          clientX: rect.left + 92,
          clientY: rect.top + 12,
          pageX: rect.left + 92,
          pageY: rect.top + 12,
        },
      },
      { keys: "[/MouseLeft]" },
    ]);
    await expect(Number(separator.getAttribute("aria-valuenow"))).toBeGreaterThan(60);
  },
};
