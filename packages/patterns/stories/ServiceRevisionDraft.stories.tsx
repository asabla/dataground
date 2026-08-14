import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { ServiceRevisionDraft } from "../src/ServiceRevisionDraft";
import "../src/styles.css";

const meta = {
  args: {
    canCreateRevision: true,
    inputSchema:
      '{\n  "type": "object",\n  "properties": {\n    "prompt": { "type": "string" }\n  }\n}',
    isolationDomainId: "iso_00000000000000000001",
    onInputSchemaChange: fn(),
    onOutputSchemaChange: fn(),
    onRequiredCapabilitiesChange: fn(),
    onRuntimeProfileChange: fn(),
    onSubmit: fn(),
    outputSchema: "",
    requiredCapabilities: "tool, usage",
    runtimeProfile: "reference/v1",
    serviceId: "svc_00000000000000000001",
  },
  component: ServiceRevisionDraft,
  tags: ["autodocs"],
  title: "Patterns/ServiceRevisionDraft",
} satisfies Meta<typeof ServiceRevisionDraft>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Ready: Story = {
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Create revision draft" }));
    await expect(args.onSubmit).toHaveBeenCalledOnce();
  },
};

export const Invalid: Story = {
  args: {
    inputSchema: "[]",
    validationErrors: { inputSchema: "Schema must be a JSON object." },
  },
};

export const Observer: Story = {
  args: {
    canCreateRevision: false,
    disabledReason: "Only service operators may create revisions.",
  },
};

export const Recovery: Story = {
  args: {
    error: {
      message: "DataGround could not confirm whether the revision draft was created.",
      retryable: true,
    },
    recoveryPending: true,
  },
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Retry revision creation" }));
    await expect(args.onSubmit).toHaveBeenCalledOnce();
  },
};

export const Created: Story = {
  args: {
    created: {
      createdAt: "2026-08-14T16:00:00Z",
      createdBy: "usr_00000000000000000001",
      id: "rev_00000000000000000001",
      revisionNumber: 2,
      runtimeProfile: "reference/v1",
      state: "draft",
      version: 1,
    },
    onOpenRevision: fn(),
  },
};
