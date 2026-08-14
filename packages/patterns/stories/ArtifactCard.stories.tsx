import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, fn, userEvent, within } from "storybook/test";
import { ArtifactCard, type ArtifactResource } from "../src/ArtifactCard";
import "../src/styles.css";

const artifact: ArtifactResource = {
  digest: "sha256:1b93a4b13f9917ba7e33ebf29560b17d50593f23bc1dfeeec961ae0cfabcb9e6",
  invocationId: "inv_00000000000000000001",
  kind: "structured-output",
  mediaType: "application/json",
  metadata: {
    createdAt: "2026-08-14T12:00:00Z",
    createdBy: "reference-runtime",
    generation: 1,
    id: "art_00000000000000000001",
    isolationDomainId: "iso_00000000000000000001",
    provenance: {
      requestCorrelationId: "cor_00000000000000000001",
      sourceRevision: "rev_00000000000000000001",
    },
    updatedAt: "2026-08-14T12:00:00.001Z",
    version: 1,
  },
  name: "reference-result.json",
  sensitive: true,
  sizeBytes: 2_097_152,
  state: "available",
};

const reference = {
  artifactId: artifact.metadata.id,
  invocationId: artifact.invocationId,
  isolationDomainId: artifact.metadata.isolationDomainId,
};

const meta = {
  args: { artifact, onRefresh: fn(), reference },
  component: ArtifactCard,
  tags: ["autodocs"],
  title: "Patterns/ArtifactCard",
} satisfies Meta<typeof ArtifactCard>;

export default meta;
type Story = StoryObj<typeof meta>;

export const AvailableSensitive: Story = {
  play: async ({ args, canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(canvas.getByRole("button", { name: "Refresh metadata" }));
    await expect(args.onRefresh).toHaveBeenCalledOnce();
  },
};

export const Pending: Story = {
  args: { artifact: { ...artifact, sensitive: false, state: "pending" } },
};

export const Failed: Story = {
  args: { artifact: { ...artifact, sensitive: false, state: "failed" } },
};

export const Deleted: Story = {
  args: { artifact: { ...artifact, sensitive: false, state: "deleted" } },
};

export const Loading: Story = {
  args: { artifact: undefined, isLoading: true },
  render: (args) => <ArtifactCard {...args} artifact={undefined} />,
};

export const Unavailable: Story = {
  args: {
    artifact: undefined,
    error: {
      correlationId: "cor_00000000000000000003",
      message: "Artifact metadata was not found.",
      retryable: false,
    },
  },
  render: (args) => <ArtifactCard {...args} artifact={undefined} />,
};

export const DegradedRefresh: Story = {
  args: {
    error: {
      correlationId: "cor_00000000000000000002",
      message: "The artifact service could not confirm newer metadata.",
      retryable: true,
    },
  },
};

export const UnknownState: Story = {
  args: { artifact: { ...artifact, kind: "future-kind", sensitive: false, state: "quarantined" } },
};

export const NarrowRightToLeft: Story = {
  decorators: [
    (Story) => (
      <div dir="rtl" style={{ maxWidth: "22rem" }}>
        <Story />
      </div>
    ),
  ],
};
