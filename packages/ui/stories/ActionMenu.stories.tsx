import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, waitFor, within } from "storybook/test";
import { ActionMenu } from "../src/ActionMenu";

const meta = {
  title: "Primitives/ActionMenu",
  component: ActionMenu,
  tags: ["autodocs"],
  args: {
    label: "More actions for revision 2",
    items: [
      { id: "audit", label: "Inspect audit" },
      { id: "retire", label: "Retire revision", isDisabled: true },
      { id: "copy", label: "Copy identifier" },
    ],
    onAction: fn(),
  },
} satisfies Meta<typeof ActionMenu>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Keyboard: Story = {
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    const trigger = canvas.getByRole("button", { name: args.label });
    trigger.focus();
    await userEvent.keyboard("{Enter}");
    const menu = await canvas.findByRole("menu", { name: args.label });
    await expect(within(menu).getByRole("menuitem", { name: "Inspect audit" })).toHaveFocus();
    await userEvent.keyboard("{ArrowDown}");
    await expect(within(menu).getByRole("menuitem", { name: "Copy identifier" })).toHaveFocus();
    await userEvent.keyboard("{Enter}");
    await expect(args.onAction).toHaveBeenCalledOnce();
    await expect(args.onAction).toHaveBeenCalledWith("copy");
    await expect(canvas.queryByRole("menu")).not.toBeInTheDocument();
    await waitFor(() => expect(trigger).toHaveFocus());
    await userEvent.keyboard("{Enter}{Escape}");
    await expect(canvas.queryByRole("menu")).not.toBeInTheDocument();
    await waitFor(() => expect(trigger).toHaveFocus());
  },
};

export const Disabled: Story = {
  args: { isDisabled: true },
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("button", { name: args.label })).toBeDisabled();
    await expect(canvas.queryByRole("menu")).not.toBeInTheDocument();
  },
};

export const Creation: Story = {
  args: {
    label: "Create resource",
    triggerLabel: "New",
    items: [
      { id: "service", label: "Agent service" },
      { id: "workspace", label: "Workspace" },
    ],
  },
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    const trigger = canvas.getByRole("button", { name: "Create resource" });
    await expect(trigger).toHaveTextContent("New");
    trigger.focus();
    await userEvent.keyboard("{Enter}");
    const menu = await canvas.findByRole("menu", { name: args.label });
    await expect(within(menu).getByRole("menuitem", { name: "Agent service" })).toHaveFocus();
    await userEvent.keyboard("{ArrowDown}{Enter}");
    await expect(args.onAction).toHaveBeenCalledWith("workspace");
    await expect(canvas.queryByRole("menu")).not.toBeInTheDocument();
    await waitFor(() => expect(trigger).toHaveFocus());
  },
};
