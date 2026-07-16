import type { Preview } from "@storybook/react-vite";
import "../src/styles.css";
import "./preview.css";

const preview: Preview = {
  decorators: [
    (Story, context) => (
      <div
        className="dg-story"
        data-density={context.globals.density}
        data-theme={context.globals.theme}
      >
        <Story />
      </div>
    ),
  ],
  globalTypes: {
    density: {
      defaultValue: "compact",
      description: "DataGround interface density",
      toolbar: {
        icon: "component",
        items: ["compact", "standard", "comfortable"],
      },
    },
    theme: {
      defaultValue: "night",
      description: "DataGround semantic theme",
      toolbar: {
        icon: "paintbrush",
        items: ["night", "light", "contrast-dark", "contrast-light"],
      },
    },
  },
  parameters: {
    a11y: {
      test: "error",
    },
    controls: {
      expanded: true,
    },
    layout: "fullscreen",
  },
};

export default preview;
