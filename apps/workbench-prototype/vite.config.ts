import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react()],
  server: { host: "127.0.0.1", hmr: false },
  preview: { host: "127.0.0.1", port: 5174, strictPort: true },
  test: { environment: "node" },
});
