import react from "@vitejs/plugin-react";
import { configDefaults, defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/v1": {
        target: "http://127.0.0.1:8082",
      },
    },
  },
  test: {
    environment: "node",
    exclude: [...configDefaults.exclude, "**/*.browser.test.tsx"],
  },
});
