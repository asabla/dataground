import type { Meta, StoryObj } from "@storybook/react-vite";
import { StatusBadge } from "../src/StatusBadge";

const meta = {
  component: StatusBadge,
  tags: ["autodocs"],
  title: "Primitives/StatusBadge",
} satisfies Meta<typeof StatusBadge>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Vocabulary: Story = {
  args: { children: "Neutral" },
  render: () => (
    <div className="dg-story-row">
      <StatusBadge>Neutral</StatusBadge>
      <StatusBadge tone="active">Active</StatusBadge>
      <StatusBadge tone="waiting">Waiting</StatusBadge>
      <StatusBadge tone="success">Succeeded</StatusBadge>
      <StatusBadge tone="warning">Warning</StatusBadge>
      <StatusBadge tone="critical">Failed</StatusBadge>
    </div>
  ),
};
