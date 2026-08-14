import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { TextField } from "../src/TextField";

const meta = {
  args: {
    description: "The stable service alias used for this invocation.",
    label: "Alias",
    onChange: fn(),
    value: "stable",
  },
  component: TextField,
  tags: ["autodocs"],
  title: "Primitives/TextField",
} satisfies Meta<typeof TextField>;

export default meta;
type Story = StoryObj<typeof meta>;

export const SingleLine: Story = {
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(canvas.getByRole("textbox", { name: "Alias" }), "-next");
    await expect(args.onChange).toHaveBeenCalled();
  },
};

export const Multiline: Story = {
  args: {
    description: "Content is submitted through the governed invocation API.",
    isMultiline: true,
    label: "Prompt",
    value: "Inspect the current workspace.",
  },
};

export const Invalid: Story = {
  args: {
    errorMessage: "Prompt is required.",
    isMultiline: true,
    isRequired: true,
    label: "Prompt",
    value: "",
  },
};

export const Disabled: Story = {
  args: { isDisabled: true },
};
