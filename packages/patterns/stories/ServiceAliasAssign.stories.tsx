import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { expect, fn, userEvent, waitFor, within } from "storybook/test";
import { ServiceAliasAssign } from "../src/ServiceAliasAssign";
import "../src/styles.css";

const meta = {
  args: {
    aliasName: "stable",
    canAssign: true,
    isolationDomainId: "iso_00000000000000000001",
    onAliasNameChange: fn(),
    onConfirm: fn(),
    onDismissConfirmation: fn(),
    onRequestConfirmation: fn(),
    revisionNumber: 2,
    serviceId: "svc_00000000000000000001",
    targetRevisionId: "rev_00000000000000000001",
    targetVersion: 2,
  },
  component: ServiceAliasAssign,
  tags: ["autodocs"],
  title: "Patterns/ServiceAliasAssign",
} satisfies Meta<typeof ServiceAliasAssign>;

export default meta;
type Story = StoryObj<typeof meta>;

export const NewAlias: Story = {
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Review routing" }));
    await expect(args.onRequestConfirmation).toHaveBeenCalledOnce();
  },
};

export const MoveAlias: Story = {
  args: {
    currentAliasId: "als_00000000000000000001",
    currentRevisionId: "rev_00000000000000000002",
    currentVersion: 4,
  },
};

export const Confirmation: Story = {
  args: {
    confirmationVisible: true,
    currentAliasId: "als_00000000000000000001",
    currentRevisionId: "rev_00000000000000000002",
    currentVersion: 4,
  },
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Confirm route change" }));
    await expect(args.onConfirm).toHaveBeenCalledOnce();
  },
};

export const Observer: Story = {
  args: {
    canAssign: false,
    disabledReason: "Only service routers may assign aliases.",
  },
};

export const Recovery: Story = {
  args: {
    error: {
      message: "DataGround could not confirm whether the service route changed.",
      outcomeUnknown: true,
      retryable: true,
    },
    recoveryPending: true,
  },
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Retry route change" }));
    await expect(args.onConfirm).toHaveBeenCalledOnce();
  },
};

export const Assigned: Story = {
  args: {
    assigned: {
      generation: 1,
      id: "als_00000000000000000001",
      name: "stable",
      revisionId: "rev_00000000000000000001",
      updatedAt: "2026-08-14T16:02:00Z",
      version: 1,
    },
    onComposeInvocation: fn(),
  },
};

export const Workbench: Story = {
  args: { compact: true },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const details = canvasElement.querySelector("details");
    await expect(details).not.toHaveAttribute("open");
    await userEvent.click(canvas.getByText("Resource details"));
    await expect(details).toHaveAttribute("open");
    await userEvent.click(canvas.getByText("Resource details"));
    await expect(details).not.toHaveAttribute("open");
  },
};

export const WorkbenchConfirmation: Story = {
  args: { compact: true, confirmationVisible: true },
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    const decision = await canvas.findByRole("dialog", { name: "Confirm service route change" });
    await expect(decision).toHaveTextContent("iso_00000000000000000001");
    await expect(decision).toHaveTextContent("revision 2");
    await userEvent.click(canvas.getByRole("button", { name: "Confirm route change" }));
    await expect(args.onConfirm).toHaveBeenCalledOnce();
  },
};

export const WorkbenchCompletionFocus: Story = {
  render: function Completion(args) {
    const [complete, setComplete] = useState(false);
    return (
      <ServiceAliasAssign
        {...args}
        compact
        confirmationVisible
        assigned={complete ? Assigned.args?.assigned : undefined}
        onConfirm={() => setComplete(true)}
      />
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Confirm route change" }));
    await waitFor(() =>
      expect(canvasElement.querySelector(".dg-service-alias-assign__assigned")).toHaveFocus(),
    );
  },
};

export const WorkbenchKeyboard: Story = {
  render: function Keyboard(args) {
    const [open, setOpen] = useState(false);
    return (
      <ServiceAliasAssign
        {...args}
        compact
        confirmationVisible={open}
        onRequestConfirmation={() => setOpen(true)}
        onDismissConfirmation={() => setOpen(false)}
      />
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const trigger = canvas.getByRole("button", { name: "Review routing" });
    await userEvent.click(trigger);
    await canvas.findByRole("dialog", { name: "Confirm service route change" });
    await waitFor(() =>
      expect(canvas.getByRole("button", { name: "Keep current route" })).toHaveFocus(),
    );
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(canvas.queryByRole("dialog")).not.toBeInTheDocument());
    await waitFor(() => expect(trigger).toHaveFocus());
  },
};

export const WorkbenchPending: Story = {
  args: { compact: true, confirmationVisible: true, isSubmitting: true },
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    const dialog = await canvas.findByRole("dialog", { name: "Confirm service route change" });
    await expect(canvas.getByRole("button", { name: "Keep current route" })).toBeDisabled();
    await expect(canvas.getByRole("button", { name: "Changing route…" })).toBeDisabled();
    await userEvent.keyboard("{Escape}");
    await expect(dialog).toBeVisible();
    await expect(args.onDismissConfirmation).not.toHaveBeenCalled();
  },
};

export const InactiveConfirmation: Story = {
  args: { compact: true, active: false, confirmationVisible: true },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).queryByRole("dialog")).not.toBeInTheDocument();
  },
};
