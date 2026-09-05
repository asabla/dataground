import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { expect, fn, userEvent, within } from "storybook/test";
import {
  type QuestionDraft,
  QuestionRequest,
  type QuestionRequestProps,
} from "../src/QuestionRequest";
import "../src/styles.css";

const question = {
  id: "qst_00000000000000000001",
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
  state: "pending",
  expiresAt: "2026-09-06T12:15:00Z",
  questions: [
    {
      id: "item_1",
      title: "Target",
      prompt: "Which environment should the report describe?",
      multiple: false,
      allowFreeText: true,
      options: [
        { id: "option_1", label: "Development", description: "The local workspace." },
        { id: "option_2", label: "Staging", description: "The staging environment." },
      ],
    },
  ],
};
function Interactive(args: QuestionRequestProps) {
  const [drafts, setDrafts] = useState<Record<string, QuestionDraft>>(args.drafts);
  const draft = drafts.item_1;
  return (
    <QuestionRequest
      {...args}
      drafts={drafts}
      complete={Boolean(draft?.useFreeText ? draft.text.trim() : draft?.optionIds.length)}
      onDraft={(id, draft) => {
        setDrafts((current) => ({ ...current, [id]: draft }));
        args.onDraft(id, draft);
      }}
    />
  );
}
const meta = {
  title: "Patterns/QuestionRequest",
  component: QuestionRequest,
  tags: ["autodocs"],
  render: (args) => <Interactive {...args} />,
  args: {
    question,
    drafts: {},
    canAnswer: true,
    complete: false,
    expired: false,
    onDraft: fn(),
    onSubmit: fn(),
    onRefresh: fn(),
  },
} satisfies Meta<typeof QuestionRequest>;
export default meta;
type Story = StoryObj<typeof meta>;
export const Pending: Story = {
  play: async ({ canvasElement, args }) => {
    const canvas = within(canvasElement);
    await expect(canvas.getByRole("button", { name: "Submit answers" })).toBeDisabled();
    await userEvent.tab();
    await expect(
      canvas.getByRole("radio", { name: "Development The local workspace." }),
    ).toHaveFocus();
    await userEvent.keyboard(" ");
    await expect(canvas.getByRole("button", { name: "Submit answers" })).toBeEnabled();
    await userEvent.click(canvas.getByRole("button", { name: "Submit answers" }));
    await expect(args.onSubmit).toHaveBeenCalled();
  },
};
export const FreeText: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("checkbox", { name: "Write my own answer for Target" }));
    await userEvent.type(
      canvas.getByRole("textbox", { name: "Answer for Target" }),
      "A custom report scope",
    );
    await expect(canvas.getByRole("button", { name: "Submit answers" })).toBeEnabled();
    await expect(
      canvas.getByRole("radio", { name: "Development The local workspace." }),
    ).toBeDisabled();
  },
};
export const Observer: Story = { args: { canAnswer: false } };
export const Expired: Story = { args: { expired: true } };
export const Recorded: Story = {
  args: { question: { ...question, state: "answered", answeredBy: "usr_00000000000000000001" } },
};
export const DeliveryUnknown: Story = {
  args: { question: { ...question, state: "delivery_unknown" } },
};
export const UncertainSubmission: Story = {
  args: {
    frozen: true,
    drafts: { item_1: { optionIds: ["option_2"], text: "", useFreeText: false } },
    error: {
      message: "The answer outcome is unknown. Refresh the question before retrying.",
      correlationId: "cor_00000000000000000001",
    },
    disabledReason: "Refresh the authoritative question state before submitting.",
  },
};
export const NarrowRightToLeft: Story = {
  decorators: [
    (Story) => (
      <div dir="rtl" style={{ maxWidth: "22rem" }}>
        <Story />
      </div>
    ),
  ],
  play: async ({ canvasElement }) => {
    const panel = canvasElement.querySelector(".dg-question-request");
    const parent = panel?.parentElement;
    if (!panel || !parent) throw new Error("question panel unavailable");
    const bounds = panel.getBoundingClientRect();
    const container = parent.getBoundingClientRect();
    await expect(bounds.left).toBeGreaterThanOrEqual(container.left);
    await expect(bounds.right).toBeLessThanOrEqual(container.right);
  },
};
