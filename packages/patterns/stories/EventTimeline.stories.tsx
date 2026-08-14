import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { EventTimeline, type TimelineEvent } from "../src/EventTimeline";
import "../src/styles.css";

const reference = {
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
};

function event(sequence: number, type: string, payload: Record<string, unknown>): TimelineEvent {
  return {
    actorId: "reference-runtime",
    correlationId: "correlation-fixture",
    id: `evt_${sequence.toString(36).padStart(20, "0")}`,
    invocationId: reference.invocationId,
    isolationDomainId: reference.isolationDomainId,
    occurredAt: `2026-08-14T12:00:0${sequence}Z`,
    payload,
    recordedAt: `2026-08-14T12:00:0${sequence}.001Z`,
    revisionId: "rev_00000000000000000001",
    schemaVersion: "dataground.event/v1",
    sequence,
    serviceId: "svc_00000000000000000001",
    type,
  };
}

// Mirrors the deterministic reference runtime's success scenario.
const successEvents = [
  event(1, "lifecycle.started", { message: "Reference runtime started." }),
  event(2, "output.text.delta", { text: "Reference runtime completed." }),
  event(3, "activity.tool.started", { name: "reference.lookup" }),
  event(4, "activity.tool.completed", { status: "succeeded" }),
  event(5, "activity.process.started", { name: "reference-process" }),
  event(6, "activity.process.completed", { exitCode: 0 }),
  event(7, "usage.recorded", { inputTokens: 12, outputTokens: 8, totalTokens: 20 }),
  event(8, "lifecycle.succeeded", { message: "Reference runtime completed." }),
];

const meta = {
  args: {
    connectionState: "current",
    events: successEvents,
    onReplay: fn(),
    reference,
  },
  component: EventTimeline,
  tags: ["autodocs"],
  title: "Patterns/EventTimeline",
} satisfies Meta<typeof EventTimeline>;

export default meta;
type Story = StoryObj<typeof meta>;

export const ReferenceSuccess: Story = {
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Replay new events" }));
    await expect(args.onReplay).toHaveBeenCalledOnce();
  },
};

export const Loading: Story = {
  args: { connectionState: "loading", events: [], isReplaying: true },
};

export const Empty: Story = {
  args: { events: [] },
};

export const DegradedReplay: Story = {
  args: {
    connectionState: "degraded",
    error: {
      correlationId: "cor_00000000000000000001",
      message: "The event service could not confirm newer journal records.",
      retryable: true,
    },
    events: successEvents.slice(0, 3),
    hiddenEventCount: 12,
  },
};

export const UnknownEvent: Story = {
  args: {
    events: [
      event(1, "lifecycle.started", { message: "Reference runtime started." }),
      event(2, "runtime.future.signal", { meaning: "safe to ignore" }),
    ],
  },
};

export const WaitingForApproval: Story = {
  args: {
    events: [
      event(1, "lifecycle.started", { message: "Runtime turn started." }),
      event(2, "interaction.approval.requested", { action: "workspace.change" }),
      event(3, "lifecycle.waiting", { reason: "approval" }),
    ],
  },
};

export const NarrowRightToLeft: Story = {
  args: { events: successEvents.slice(0, 4) },
  decorators: [
    (Story) => (
      <div dir="rtl" style={{ maxWidth: "22rem" }}>
        <Story />
      </div>
    ),
  ],
};
