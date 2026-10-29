import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { expect, fn, userEvent, within } from "storybook/test";
import { ApprovalRequest, type ApprovalResource } from "../src/ApprovalRequest";
import "../src/styles.css";

const pendingApproval: ApprovalResource = {
  id: "apr_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
  invocationId: "inv_00000000000000000001",
  requestedAction: "workspace.change",
  state: "pending",
  version: 1,
  createdAt: "2026-08-14T12:00:00Z",
  updatedAt: "2026-08-14T12:00:00Z",
};

const meta = {
  args: {
    approval: pendingApproval,
    canResolve: true,
    onDecision: fn(),
    onRefresh: fn(),
  },
  component: ApprovalRequest,
  tags: ["autodocs"],
  title: "Patterns/ApprovalRequest",
} satisfies Meta<typeof ApprovalRequest>;

export default meta;
type Story = StoryObj<typeof meta>;
export const CompletionFocus: Story = {
  render: function Completion(args) {
    const [recorded, setRecorded] = useState(false);
    return (
      <ApprovalRequest
        {...args}
        compact
        approval={{ ...args.approval, state: recorded ? "resolved" : "pending" }}
        onDecision={() => setRecorded(true)}
      />
    );
  },
  play: async ({ canvasElement }) => {
    await userEvent.click(within(canvasElement).getByRole("button", { name: "Deny request" }));
    await expect(canvasElement.querySelector(".dg-approval-request__heading")).toHaveFocus();
  },
};

export const Pending: Story = {
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Approve request" }));
    await expect(args.onDecision).toHaveBeenCalledWith("approve");
  },
};

export const Observer: Story = {
  args: {
    canResolve: false,
    disabledReason: "You can observe this request but do not have approval authority.",
  },
};

export const Resolved: Story = {
  args: {
    approval: {
      ...pendingApproval,
      decision: "approve",
      resolvedAt: "2026-08-14T12:01:00Z",
      resolvedBy: "usr_00000000000000000001",
      state: "resolved",
      updatedAt: "2026-08-14T12:01:00Z",
      version: 2,
    },
  },
};

export const Delivering: Story = {
  args: {
    approval: {
      ...pendingApproval,
      decision: "approve",
      resolvedBy: "usr_00000000000000000001",
      state: "delivering",
      updatedAt: "2026-08-14T12:01:30Z",
      version: 3,
    },
  },
};

export const DeliveredDenial: Story = {
  args: {
    approval: {
      ...pendingApproval,
      decision: "deny",
      resolvedBy: "usr_00000000000000000001",
      state: "delivered",
      updatedAt: "2026-08-14T12:02:00Z",
      version: 4,
    },
  },
};

export const AmbiguousSubmission: Story = {
  args: {
    error: {
      correlationId: "cor_00000000000000000001",
      message: "The approval service could not confirm whether the decision committed.",
      retryable: true,
    },
    recoveryDecision: "approve",
  },
};

export const UnknownServerState: Story = {
  args: {
    approval: {
      ...pendingApproval,
      requestedAction: "runtime.future-action",
      state: "future-state",
      version: 8,
    },
  },
};

export const NarrowRightToLeft: Story = {
  decorators: [
    (Story) => (
      <div dir="rtl" style={{ maxWidth: "22rem" }}>
        <Story />
      </div>
    ),
  ],
};

export const Expired: Story = {
  args: {
    approval: {
      ...pendingApproval,
      state: "expired",
      version: 2,
      expiresAt: "2026-08-14T12:10:00Z",
      closedAt: "2026-08-14T12:10:00Z",
      closeReason: "expired",
      updatedAt: "2026-08-14T12:10:00Z",
    },
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText("Request expired")).toBeVisible();
    await expect(canvas.queryByRole("button", { name: "Approve request" })).not.toBeInTheDocument();
  },
};
export const DeliveryUnknown: Story = {
  args: {
    approval: {
      ...pendingApproval,
      state: "delivery_unknown",
      version: 4,
      decision: "deny",
      resolvedBy: "controller",
      resolvedAt: "2026-08-14T12:01:00Z",
      expiresAt: "2026-08-14T12:10:00Z",
      closedAt: "2026-08-14T12:10:00Z",
      closeReason: "expired",
      updatedAt: "2026-08-14T12:10:00Z",
    },
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText("Delivery unknown")).toBeVisible();
    await expect(canvas.getByText(/It cannot be sent again/)).toBeVisible();
    await expect(canvas.queryByRole("button", { name: "Deny request" })).not.toBeInTheDocument();
  },
};

export const Workbench: Story = {
  args: { compact: true, approval: { ...pendingApproval, expiresAt: "2026-10-29T12:10:00Z" } },
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText(pendingApproval.id)).not.toBeVisible();
    await expect(canvas.getByText("2026-10-29T12:10:00Z")).toBeVisible();
    await userEvent.click(canvas.getByRole("button", { name: "Deny request" }));
    await expect(args.onDecision).toHaveBeenCalledWith("deny");
    await userEvent.click(canvas.getByText("Resource details"));
    await expect(canvas.getByText(pendingApproval.id)).toBeVisible();
  },
};
export const WorkbenchUncertain: Story = {
  args: { ...AmbiguousSubmission.args, compact: true },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("alert")).toBeVisible();
    await expect(canvas.getByRole("button", { name: "Retry approval" })).toBeEnabled();
    await expect(canvas.queryByRole("button", { name: "Deny request" })).not.toBeInTheDocument();
    await expect(canvas.getByText(pendingApproval.id)).not.toBeVisible();
  },
};

export const WorkbenchRecorded: Story = {
  args: { ...Resolved.args, compact: true },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByText("Approved")).toBeVisible();
    await expect(canvas.getByText("usr_00000000000000000001")).not.toBeVisible();
    await expect(canvas.getByText("Record updated")).not.toBeVisible();
    await userEvent.click(canvas.getByText("Resource details"));
    await expect(
      canvas.getByRole("button", { name: "Copy deciding actor identifier" }),
    ).toBeVisible();
  },
};
