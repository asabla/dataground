import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { expect, fn, userEvent, within } from "storybook/test";
import { Button } from "../src/Button";
import { CopyButton } from "../src/CopyButton";

const meta = {
  args: {
    label: "service identifier",
    value: "svc_00000000000000000001",
    writeText: fn(async () => {}),
  },
  component: CopyButton,
  tags: ["autodocs"],
  title: "Primitives/CopyButton",
} satisfies Meta<typeof CopyButton>;
export default meta;
type Story = StoryObj<typeof meta>;
export const Ready: Story = {
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    canvas.getByRole("button", { name: "Copy service identifier" }).focus();
    await userEvent.keyboard("{Enter}");
    await expect(args.writeText).toHaveBeenCalledWith(args.value);
    await expect(canvas.getByRole("status")).toHaveTextContent("service identifier copied.");
  },
};
export const Unavailable: Story = {
  args: {
    writeText: fn(async () => {
      throw new Error("Permission denied with private browser details");
    }),
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Copy service identifier" }));
    await expect(canvas.getByRole("alert")).toHaveTextContent("Select the value and copy it.");
    await expect(canvas.queryByText(/private browser details/)).not.toBeInTheDocument();
  },
};
function ChangingResource() {
  const [value, setValue] = useState("first");
  const [complete, setComplete] = useState<(() => void) | undefined>();
  return (
    <>
      <CopyButton
        label="identifier"
        value={value}
        writeText={() => new Promise<void>((resolve) => setComplete(() => resolve))}
      />
      <Button
        onPress={() => {
          setValue("second");
          complete?.();
        }}
      >
        Select another resource
      </Button>
    </>
  );
}
export const ChangedResource: Story = {
  render: () => <ChangingResource />,
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Copy identifier" }));
    await userEvent.click(canvas.getByRole("button", { name: "Select another resource" }));
    await expect(canvas.getByRole("status")).toBeEmptyDOMElement();
    await expect(canvas.getByRole("button", { name: "Copy identifier" })).toBeEnabled();
  },
};
