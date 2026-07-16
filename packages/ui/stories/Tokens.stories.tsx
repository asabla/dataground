import type { Meta, StoryObj } from "@storybook/react-vite";
import type { CSSProperties } from "react";

const semanticColors = [
  "color.canvas",
  "color.surface.default",
  "color.surface.raised",
  "color.action.primary",
  "color.status.waiting",
  "color.status.success",
  "color.status.critical",
] as const;

const meta = {
  parameters: {
    docs: {
      description: {
        component: "Semantic roles remain stable while themes supply their concrete values.",
      },
    },
  },
  title: "Foundations/Semantic tokens",
} satisfies Meta;

export default meta;
type Story = StoryObj<typeof meta>;

export const ColorRoles: Story = {
  render: () => (
    <div className="dg-token-grid">
      {semanticColors.map((token) => {
        const cssName = `--dg-${token
          .replaceAll(".", "-")
          .replaceAll(/([a-z0-9])([A-Z])/gu, "$1-$2")
          .toLowerCase()}`;
        return (
          <div
            className="dg-token-swatch"
            key={token}
            style={{ "--swatch": `var(${cssName})` } as CSSProperties}
          >
            <code>{token}</code>
          </div>
        );
      })}
    </div>
  ),
};
