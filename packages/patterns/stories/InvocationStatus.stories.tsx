import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { expect, fn, userEvent, within } from "storybook/test";
import { InvocationStatus, type InvocationStatusResource } from "../src/InvocationStatus";
import "../src/styles.css";

const invocation: InvocationStatusResource = {
  alias: "stable",
  artifactIds: ["art_00000000000000000001"],
  correlationId: "cor_00000000000000000001",
  metadata: {
    createdAt: "2026-08-14T12:00:00Z",
    createdBy: "reference-runtime",
    generation: 2,
    id: "inv_00000000000000000001",
    isolationDomainId: "iso_00000000000000000001",
    updatedAt: "2026-08-14T12:00:01Z",
    version: 2,
  },
  operationId: "op_00000000000000000001",
  revisionId: "rev_00000000000000000001",
  serviceId: "svc_00000000000000000001",
  state: "running",
  usage: { inputTokens: 12, outputTokens: 8, totalTokens: 20 },
};

const operation = {
  attempt: 1,
  command: "invoke",
  correlationId: invocation.correlationId,
  desiredState: "succeeded",
  kind: "invocation-execution",
  metadata: { ...invocation.metadata, id: invocation.operationId },
  observedState: "running",
  stateMachineVersion: 2,
};

const reference = {
  invocationId: invocation.metadata.id,
  isolationDomainId: invocation.metadata.isolationDomainId,
};

const meta = {
  args: {
    canCancel: true,
    invocation,
    onConfirmCancellation: fn(),
    onDismissCancellation: fn(),
    onInspectArtifact: fn(),
    onRefresh: fn(),
    onRequestCancellation: fn(),
    operation,
    reference,
  },
  component: InvocationStatus,
  tags: ["autodocs"],
  title: "Patterns/InvocationStatus",
} satisfies Meta<typeof InvocationStatus>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Running: Story = {
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Request cancellation" }));
    await expect(args.onRequestCancellation).toHaveBeenCalledOnce();
  },
};

export const ConfirmCancellation: Story = {
  args: { cancellationConfirmationVisible: true },
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Confirm cancellation" }));
    await expect(args.onConfirmCancellation).toHaveBeenCalledOnce();
  },
};

export const Cancelling: Story = {
  args: {
    invocation: { ...invocation, state: "cancelling" },
    operation: {
      ...operation,
      command: "cancel",
      desiredState: "cancelled",
      observedState: "cancelling",
    },
  },
};

export const Cancelled: Story = {
  args: {
    invocation: { ...invocation, completedAt: "2026-08-14T12:00:02Z", state: "cancelled" },
    operation: {
      ...operation,
      command: "cancel",
      desiredState: "cancelled",
      observedState: "cancelled",
    },
  },
};

export const Succeeded: Story = {
  args: {
    invocation: {
      ...invocation,
      completedAt: "2026-08-14T12:00:02Z",
      state: "succeeded",
    },
    operation: { ...operation, observedState: "succeeded" },
  },
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      canvas.getByRole("button", {
        name: `Inspect metadata for ${invocation.artifactIds[0]}`,
      }),
    );
    await expect(args.onInspectArtifact).toHaveBeenCalledWith(invocation.artifactIds[0]);
  },
};

export const Failed: Story = {
  args: {
    invocation: {
      ...invocation,
      error: {
        code: "RUNTIME_FAILED",
        correlationId: "cor_00000000000000000002",
        message: "The runtime reported a safe terminal failure.",
        retryable: false,
      },
      state: "failed",
    },
    operation: {
      ...operation,
      errorClassification: "terminal",
      observedState: "failed",
    },
  },
};

export const Observer: Story = {
  args: { canCancel: false },
};

export const Degraded: Story = {
  args: {
    error: {
      correlationId: "cor_00000000000000000002",
      message: "The operation service could not confirm newer state.",
      retryable: true,
    },
    operation: undefined,
  },
};

export const CancellationRecovery: Story = {
  args: {
    cancellationRecovery: true,
    error: {
      message: "DataGround could not confirm whether cancellation was accepted.",
      retryable: true,
    },
  },
};

export const UnknownState: Story = {
  args: { invocation: { ...invocation, state: "quarantined" } },
};

export const Loading: Story = {
  args: { invocation: undefined, isLoading: true, operation: undefined },
  render: (args) => <InvocationStatus {...args} invocation={undefined} operation={undefined} />,
};

export const Unavailable: Story = {
  args: {
    error: {
      correlationId: "cor_00000000000000000003",
      message: "Invocation state could not be read.",
      retryable: true,
    },
    invocation: undefined,
    operation: undefined,
  },
  render: (args) => <InvocationStatus {...args} invocation={undefined} operation={undefined} />,
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

export const CompactRunning: Story = {
  args: { compact: true },
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("heading", { name: "Run outcome" })).toBeVisible();
    await expect(canvas.getByText(invocation.operationId)).not.toBeVisible();
    await userEvent.click(canvas.getByRole("button", { name: "Request cancellation" }));
    await expect(args.onRequestCancellation).toHaveBeenCalledOnce();
  },
};
export const CompactUncertainCancellation: Story = {
  args: {
    compact: true,
    cancellationRecovery: true,
    error: { message: "Submission outcome unknown", retryable: true, correlationId: "cor_review" },
  },
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Retry cancellation" }));
    await expect(args.onConfirmCancellation).toHaveBeenCalledOnce();
  },
};

export const WorkbenchCancellation: Story = {
  args: { compact: true },
  render: function Cancellation(args) {
    const [confirmation, setConfirmation] = useState(false);
    const [cancelled, setCancelled] = useState(false);
    return (
      <InvocationStatus
        {...args}
        cancellationConfirmationVisible={confirmation}
        invocation={{ ...invocation, state: cancelled ? "cancelled" : "running" }}
        operation={{
          ...operation,
          command: cancelled ? "cancel" : "invoke",
          desiredState: cancelled ? "cancelled" : "succeeded",
          observedState: cancelled ? "cancelled" : "running",
        }}
        onRequestCancellation={() => setConfirmation(true)}
        onDismissCancellation={() => setConfirmation(false)}
        onConfirmCancellation={() => {
          setConfirmation(false);
          setCancelled(true);
        }}
      />
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const request = canvas.getByRole("button", { name: "Request cancellation" });
    await userEvent.click(request);
    const dialog = await canvas.findByRole("dialog", { name: "Cancel this run?" });
    await expect(within(dialog).getByRole("button", { name: "Keep running" })).toHaveFocus();
    await expect(within(dialog).getByText(reference.isolationDomainId)).toBeVisible();
    await userEvent.keyboard("{Escape}");
    await expect(canvas.queryByRole("dialog")).not.toBeInTheDocument();
    await expect(request).toHaveFocus();
    await userEvent.click(request);
    await userEvent.click(canvas.getByRole("button", { name: "Confirm cancellation" }));
    await expect(canvas.getByText("Invocation cancelled")).toBeVisible();
    await expect(canvas.getByRole("heading", { name: "Run outcome" }).parentElement).toHaveFocus();
  },
};

export const WorkbenchCancellationPending: Story = {
  args: { compact: true, isCancelling: true },
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    const dialog = await canvas.findByRole("dialog", { name: "Cancel this run?" });
    await expect(within(dialog).getByRole("button", { name: "Keep running" })).toBeDisabled();
    await expect(
      within(dialog).getByRole("button", { name: "Submitting cancellation…" }),
    ).toBeDisabled();
    await userEvent.keyboard("{Escape}");
    await expect(dialog).toBeVisible();
    await expect(args.onDismissCancellation).not.toHaveBeenCalled();
  },
};

export const WorkbenchCancellationInactive: Story = {
  args: { compact: true, active: false, cancellationConfirmationVisible: true },
  play: async ({ canvasElement }) => {
    await expect(within(canvasElement).queryByRole("dialog")).not.toBeInTheDocument();
  },
};
