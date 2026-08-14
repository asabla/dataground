import type { Meta, StoryObj } from "@storybook/react-vite";
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
