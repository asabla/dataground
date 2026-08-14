import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { InvocationComposer } from "../src/InvocationComposer";
import "../src/styles.css";

const schema = {
  description: "Supply the governed prompt accepted by this service alias.",
  fields: [
    {
      description: "What the agent should work on.",
      key: "prompt",
      label: "Prompt",
      maxLength: 262_144,
      minLength: 1,
      required: true,
    },
  ],
  title: "Agent prompt",
};

const meta = {
  args: {
    alias: "stable",
    canInvoke: true,
    onAliasChange: fn(),
    onSubmit: fn(),
    onValueChange: fn(),
    schema,
    target: {
      isolationDomainId: "iso_00000000000000000001",
      serviceId: "svc_00000000000000000001",
    },
    values: { prompt: "Inspect the current workspace." },
  },
  component: InvocationComposer,
  tags: ["autodocs"],
  title: "Patterns/InvocationComposer",
} satisfies Meta<typeof InvocationComposer>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Ready: Story = {
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Start invocation" }));
    await expect(args.onSubmit).toHaveBeenCalledOnce();
  },
};

export const Invalid: Story = {
  args: {
    alias: "INVALID",
    validationErrors: {
      alias: "Use lowercase letters, numbers, and internal hyphens.",
      prompt: "Prompt is required.",
    },
    values: { prompt: "" },
  },
};

export const Observer: Story = {
  args: {
    canInvoke: false,
    disabledReason: "Only service operators may create invocations.",
  },
};

export const Recovery: Story = {
  args: {
    error: {
      message: "DataGround could not confirm whether the invocation was accepted.",
      retryable: true,
    },
    recoveryPending: true,
  },
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Retry invocation" }));
    await expect(args.onSubmit).toHaveBeenCalledOnce();
  },
};

export const Accepted: Story = {
  args: {
    accepted: {
      invocationId: "inv_00000000000000000001",
      operationId: "op_00000000000000000001",
      state: "accepted",
    },
    onOpenInvocation: fn(),
  },
};

export const UnsupportedContract: Story = {
  args: {
    schema: undefined,
    schemaError: "This input contract is not supported by the Workbench composer.",
  },
};
