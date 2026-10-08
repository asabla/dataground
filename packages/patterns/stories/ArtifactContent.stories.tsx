import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { expect, fn, userEvent, within } from "storybook/test";
import { ArtifactContent, type ArtifactContentProps } from "../src/ArtifactContent";
import "../src/styles.css";

function ControlledContent(props: ArtifactContentProps) {
  const [text, setText] = useState<string>();
  return (
    <ArtifactContent
      {...props}
      text={text}
      onRead={() => {
        setText("<script>untrusted content</script>");
        props.onRead?.();
      }}
      onHide={() => {
        setText(undefined);
        props.onHide?.();
      }}
    />
  );
}
const meta = {
  args: { onRead: fn(), onHide: fn() },
  component: ArtifactContent,
  tags: ["autodocs"],
  title: "Patterns/ArtifactContent",
} satisfies Meta<typeof ArtifactContent>;
export default meta;
type Story = StoryObj<typeof meta>;

export const ExplicitRead: Story = {
  render: (args) => <ControlledContent {...args} />,
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.tab();
    await expect(canvas.getByRole("button", { name: "Read content" })).toHaveFocus();
    await userEvent.keyboard("{Enter}");
    await expect(args.onRead).toHaveBeenCalledOnce();
    await expect(canvas.getByRole("status")).toHaveTextContent("Content verified.");
    await expect(canvas.getByRole("button", { name: "Hide content" })).toHaveFocus();
    await userEvent.tab({ shift: true });
    await expect(canvas.getByRole("region", { name: "Artifact text" })).toHaveFocus();
    await userEvent.tab();
    await userEvent.keyboard("{Enter}");
    await expect(args.onHide).toHaveBeenCalledOnce();
    await expect(canvas.queryByRole("region", { name: "Artifact text" })).not.toBeInTheDocument();
    await expect(canvas.getByRole("button", { name: "Read content" })).toHaveFocus();
  },
};
export const Reading: Story = { args: { isLoading: true } };
export const Denied: Story = {
  args: {
    error: {
      message: "Your current access does not allow this content read.",
      correlationId: "cor_00000000000000000001",
    },
  },
};
export const Binary: Story = {
  args: {
    download: { url: "blob:verified-example", filename: "artifact.bin" },
    previewMessage: "Content is verified. This file type is available as a download.",
  },
};
export const Unavailable: Story = {
  args: { unavailableMessage: "Content can be read only when the artifact is available." },
};
export const NarrowText: Story = {
  args: { text: "Long verified content ".repeat(1000) },
  decorators: [
    (Story) => (
      <div style={{ maxWidth: "22rem" }}>
        <Story />
      </div>
    ),
  ],
};
