import react from "@vitejs/plugin-react";
import { playwright } from "@vitest/browser-playwright";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react()],
  test: {
    attachmentsDir: "../../.cache/workbench-browser-attachments",
    include: ["src/**/*.browser.test.tsx"],
    browser: {
      enabled: true,
      headless: true,
      screenshotDirectory: "../../.cache/workbench-browser",
      instances: [{ browser: "chromium" }],
      provider: playwright({}),
    },
  },
});
