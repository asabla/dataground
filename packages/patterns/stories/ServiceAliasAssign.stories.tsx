import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
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
