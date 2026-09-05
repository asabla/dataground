import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { expect, fn, userEvent, within } from "storybook/test";
import { InvocationResult, type InvocationResultProps } from "../src/InvocationResult";
import "../src/styles.css";

function ControlledResult(props: InvocationResultProps) {
  const [text, setText] = useState(props.text);
  return (
    <InvocationResult
      {...props}
      text={text}
      onShow={() => {
        setText('{"output":"<script>untrusted</script>"}');
        props.onShow?.();
      }}
      onHide={() => {
        setText(undefined);
        props.onHide?.();
      }}
    />
  );
}
const meta = {
  args: { onShow: fn(), onHide: fn() },
  component: InvocationResult,
  tags: ["autodocs"],
  title: "Patterns/InvocationResult",
} satisfies Meta<typeof InvocationResult>;
export default meta;
type Story = StoryObj<typeof meta>;

export const ExplicitRead: Story = {
  render: (args) => <ControlledResult {...args} />,
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.tab();
    const button = canvas.getByRole("button", { name: "Show result" });
    await expect(button).toHaveFocus();
    await userEvent.keyboard("{Enter}");
    const result = canvas.getByRole("region", { name: "Invocation result JSON" });
    await expect(result).toHaveTextContent("<script>untrusted</script>");
    await expect(args.onShow).toHaveBeenCalledOnce();
    await expect(canvas.getByRole("button", { name: "Hide result" })).toHaveFocus();
    await userEvent.tab({ shift: true });
    await expect(result).toHaveFocus();
    await userEvent.tab();
    await userEvent.keyboard("{Enter}");
    await expect(
      canvas.queryByRole("region", { name: "Invocation result JSON" }),
    ).not.toBeInTheDocument();
    await expect(canvas.getByRole("button", { name: "Show result" })).toHaveFocus();
    await expect(args.onHide).toHaveBeenCalledOnce();
  },
};
export const Loading: Story = { args: { isLoading: true } };
export const Denied: Story = {
  args: {
    error: {
      message: "Access to this invocation is denied.",
      correlationId: "cor_00000000000000000001",
    },
  },
};
export const StructuredResult: Story = {
  args: {
    text: JSON.stringify(
      { output: { count: 0, enabled: false, rows: [{ name: "Sample", value: 12 }] } },
      null,
      2,
    ),
  },
};
