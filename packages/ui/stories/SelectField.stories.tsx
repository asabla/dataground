import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { expect, fn, userEvent, within } from "storybook/test";
import { SelectField, type SelectFieldProps } from "../src/SelectField";

function ControlledSelect(props: SelectFieldProps) {
  const [value, setValue] = useState(props.value);
  return (
    <SelectField
      {...props}
      onChange={(next) => {
        setValue(next);
        props.onChange?.(next);
      }}
      value={value}
    />
  );
}

const meta = {
  args: {
    description: "Choose whether to include details.",
    label: "Details",
    onChange: fn(),
    options: [
      { label: "True", value: "true" },
      { label: "False", value: "false" },
    ],
    value: "",
  },
  component: SelectField,
  render: (args) => <ControlledSelect {...args} />,
  tags: ["autodocs"],
  title: "Primitives/SelectField",
} satisfies Meta<typeof SelectField>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Selection: Story = {
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    const select = canvas.getByRole("combobox", { name: "Details" });
    await userEvent.tab();
    await expect(select).toHaveFocus();
    await userEvent.selectOptions(select, "false");
    await expect(select).toHaveValue("false");
    await expect(args.onChange).toHaveBeenCalledWith("false");
  },
};
export const Invalid: Story = { args: { errorMessage: "Choose a value.", isRequired: true } };
export const Disabled: Story = { args: { isDisabled: true, value: "false" } };
