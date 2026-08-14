import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { AgentServiceCreate } from "../src/AgentServiceCreate";
import "../src/styles.css";

const meta = {
  args: {
    canCreate: true,
    description: "A governed research service.",
    isolationDomainId: "iso_00000000000000000001",
    name: "Research assistant",
    onDescriptionChange: fn(),
    onNameChange: fn(),
    onSubmit: fn(),
  },
  component: AgentServiceCreate,
  tags: ["autodocs"],
  title: "Patterns/AgentServiceCreate",
} satisfies Meta<typeof AgentServiceCreate>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Ready: Story = {
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Create service" }));
    await expect(args.onSubmit).toHaveBeenCalledOnce();
  },
};

export const Invalid: Story = {
  args: {
    name: "",
    validationErrors: { name: "Service name is required." },
  },
};

export const Observer: Story = {
  args: {
    canCreate: false,
    disabledReason: "Only service operators may create services.",
  },
};

export const Recovery: Story = {
  args: {
    error: {
      message: "DataGround could not confirm whether the service was created.",
      retryable: true,
    },
    recoveryPending: true,
  },
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Retry service creation" }));
    await expect(args.onSubmit).toHaveBeenCalledOnce();
  },
};

export const Created: Story = {
  args: {
    created: {
      createdAt: "2026-08-14T12:00:00Z",
      createdBy: "usr_00000000000000000001",
      id: "svc_00000000000000000001",
      name: "Research assistant",
      version: 1,
    },
    onOpenService: fn(),
  },
};
