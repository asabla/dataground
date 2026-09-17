import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { expect, fn, userEvent, within } from "storybook/test";
import { ResourceAudit, type ResourceAuditProps } from "../src/ResourceAudit";
import "../src/styles.css";

const items: NonNullable<ResourceAuditProps["items"]> = [
  {
    id: "ard_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    source: "invocation-authorization",
    recordedAt: "2026-09-17T12:00:00Z",
    actorId: "<script>untrusted</script>",
    action: "run",
    outcome: "allowed",
    correlationId: "cor_00000000000000000001",
    operationId: "op_00000000000000000001",
    policySetId: "reviewed-policy",
    policyDigest: `sha256:${"a".repeat(64)}`,
  },
];
function ControlledAudit(props: ResourceAuditProps) {
  const [visible, setVisible] = useState(false);
  return (
    <ResourceAudit
      {...props}
      items={visible ? items : undefined}
      receiptId={visible ? "arr_00000000000000000001" : undefined}
      onRead={() => {
        setVisible(true);
        props.onRead?.();
      }}
      onHide={() => {
        setVisible(false);
        props.onHide?.();
      }}
    />
  );
}
const meta = {
  component: ResourceAudit,
  args: {
    title: "Invocation audit",
    resourceId: "inv_00000000000000000001",
    onRead: fn(),
    onHide: fn(),
    onNext: fn(),
  },
  tags: ["autodocs"],
  title: "Patterns/ResourceAudit",
} satisfies Meta<typeof ResourceAudit>;
export default meta;
type Story = StoryObj<typeof meta>;
export const ExplicitRead: Story = {
  render: (args) => <ControlledAudit {...args} />,
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.tab();
    await expect(canvas.getByRole("button", { name: "Show audit" })).toHaveFocus();
    await userEvent.keyboard("{Enter}");
    await expect(canvas.getByText("<script>untrusted</script>")).toBeVisible();
    await expect(args.onRead).toHaveBeenCalledOnce();
    await expect(canvas.getByRole("button", { name: "Hide audit" })).toHaveFocus();
    await userEvent.keyboard("{Enter}");
    await expect(canvas.queryByText("<script>untrusted</script>")).not.toBeInTheDocument();
    await expect(canvas.getByRole("button", { name: "Show audit" })).toHaveFocus();
    await expect(args.onHide).toHaveBeenCalledOnce();
  },
};
export const Records: Story = {
  args: { items, receiptId: "arr_00000000000000000001", hasNextPage: true },
};
export const Empty: Story = { args: { items: [], receiptId: "arr_00000000000000000001" } };
export const Loading: Story = { args: { isLoading: true } };
export const Denied: Story = {
  args: {
    error: {
      message: "You do not have access to this resource's audit records.",
      correlationId: "cor_00000000000000000001",
    },
  },
};
export const Unavailable: Story = {
  args: {
    error: {
      message: "Audit records are unavailable for this connection. Try again later.",
    },
  },
};
