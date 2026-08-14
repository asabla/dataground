import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { ServiceRevisionPublish } from "../src/ServiceRevisionPublish";
import "../src/styles.css";

const meta = {
  args: {
    canPublish: true,
    createdAt: "2026-08-14T16:00:00Z",
    createdBy: "usr_00000000000000000001",
    hasInputSchema: true,
    hasOutputSchema: false,
    isolationDomainId: "iso_00000000000000000001",
    onConfirm: fn(),
    onDismissConfirmation: fn(),
    onRequestConfirmation: fn(),
    requiredCapabilities: ["tool", "usage"],
    revisionId: "rev_00000000000000000001",
    revisionNumber: 2,
    runtimeProfile: "reference/v1",
    serviceId: "svc_00000000000000000001",
    version: 1,
  },
  component: ServiceRevisionPublish,
  tags: ["autodocs"],
  title: "Patterns/ServiceRevisionPublish",
} satisfies Meta<typeof ServiceRevisionPublish>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Draft: Story = {
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Review publication" }));
    await expect(args.onRequestConfirmation).toHaveBeenCalledOnce();
  },
};

export const Confirmation: Story = {
  args: { confirmationVisible: true },
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Confirm publication" }));
    await expect(args.onConfirm).toHaveBeenCalledOnce();
  },
};

export const Observer: Story = {
  args: {
    canPublish: false,
    disabledReason: "Only service publishers may publish revisions.",
  },
};

export const Recovery: Story = {
  args: {
    error: {
      message: "DataGround could not confirm whether the revision was published.",
      retryable: true,
    },
    recoveryPending: true,
  },
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Retry publication" }));
    await expect(args.onConfirm).toHaveBeenCalledOnce();
  },
};

export const Published: Story = {
  args: {
    onAssignAlias: fn(),
    published: { publishedAt: "2026-08-14T16:01:00Z", version: 2 },
  },
};
