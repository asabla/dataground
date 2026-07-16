import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { Button } from "../src/Button";

const meta = {
  args: {
    children: "Create revision",
    onPress: fn(),
  },
  component: Button,
  parameters: {
    docs: {
      description: {
        component:
          "Actions state their consequence. Primary emphasis is reserved for the next safe action.",
      },
    },
  },
  tags: ["autodocs"],
  title: "Primitives/Button",
} satisfies Meta<typeof Button>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Primary: Story = {
  args: { variant: "primary" },
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Create revision" }));
    await expect(args.onPress).toHaveBeenCalledOnce();
  },
};

export const Variants: Story = {
  render: () => (
    <div className="dg-story-row">
      <Button variant="primary">Create revision</Button>
      <Button variant="secondary">Save draft</Button>
      <Button variant="quiet">Inspect policy</Button>
      <Button variant="danger">Cancel rollout</Button>
      <Button isDisabled>Unavailable</Button>
    </div>
  ),
};
