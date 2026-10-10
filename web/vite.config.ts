import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// Build artifacts land in web/dist, which the Go binary embeds via go:embed.
// Assets use absolute paths: the binary serves the SPA at /, and relative
// paths broke deep links such as /groups/1 (assets resolved under /groups/).
export default defineConfig({
  plugins: [react()],
  base: "/",
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: "http://localhost:8080",
        changeOrigin: true,
      },
      "/uploads": {
        target: "http://localhost:8080",
        changeOrigin: true,
      },
    },
  },
  // Vitest (npm test): component tests next to the code. Browser tests
  // live in e2e/ and run with Playwright (make e2e).
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}"],
    setupFiles: ["./src/test/setup.ts"],
  },
});
