import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { expect, userEvent, within } from "storybook/test";
import { Button } from "../src/Button";
import { Dialog, type DialogProps } from "../src/Dialog";

function Example(props: DialogProps) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button onPress={() => setOpen(true)}>Review decision</Button>
      <Dialog {...props} isOpen={open} onClose={() => setOpen(false)}>
        {props.children}
        <div className="dg-dialog__actions">
          <Button autoFocus isDisabled={props.isDismissDisabled} onPress={() => setOpen(false)}>
            Keep draft
          </Button>
          <Button isDisabled={props.isDismissDisabled} onPress={() => setOpen(false)}>
            Confirm
          </Button>
        </div>
      </Dialog>
    </>
  );
}
const meta = {
  title: "Primitives/Dialog",
  component: Dialog,
  tags: ["autodocs"],
  args: {
    title: "Confirm publication",
    isOpen: false,
    onClose: () => {},
    children: <p>Publish this exact revision. Route assignment is a separate action.</p>,
  },
  render: (args) => <Example {...args} />,
} satisfies Meta<typeof Dialog>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Decision: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const trigger = canvas.getByRole("button", { name: "Review decision" });
    trigger.focus();
    await userEvent.keyboard("{Enter}");
    const dialog = await canvas.findByRole("dialog", { name: "Confirm publication" });
    const keep = within(dialog).getByRole("button", { name: "Keep draft" });
    await expect(keep).toHaveFocus();
    await userEvent.tab();
    await expect(within(dialog).getByRole("button", { name: "Confirm" })).toHaveFocus();
    await userEvent.tab();
    await expect(keep).toHaveFocus();
    await userEvent.keyboard("{Escape}");
    await expect(canvas.queryByRole("dialog")).not.toBeInTheDocument();
    await expect(trigger).toHaveFocus();
    await userEvent.keyboard("{Enter}");
    await userEvent.click(canvas.getByRole("button", { name: "Keep draft" }));
    await expect(trigger).toHaveFocus();
  },
};

export const Pending: Story = {
  args: { isDismissDisabled: true, children: <p role="status">Publication is in progress.</p> },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Review decision" }));
    await userEvent.keyboard("{Escape}");
    const dialog = canvas.getByRole("dialog");
    await expect(dialog).toBeVisible();
    await expect(within(dialog).getByRole("button", { name: "Keep draft" })).toBeDisabled();
    await expect(dialog.contains(document.activeElement)).toBe(true);
  },
};

export const LongContent: Story = {
  args: {
    title: "Review the quarterly revenue reconciliation and regional exception analysis revision",
    children: (
      <>
        <p>Review the exact scope and immutable revision before publication.</p>
        {Array.from(
          { length: 12 },
          (_, index) => `Review item ${index + 1}: inspect governed data.`,
        ).map((detail) => (
          <p key={detail}>{detail}</p>
        ))}
      </>
    ),
  },
};
