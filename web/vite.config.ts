import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// Build artifacts land in web/dist, which the Go binary embeds via go:embed.
// Assets use absolute paths: the binary serves the SPA at /, and relative
// paths broke deep links such as /groups/1 (assets resolved under /groups/).
export default defineConfig({
  plugins: [react(), tailwindcss()],
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
  // live in e2e/ and run with Playwright (make e2e). The test script in
  // package.json passes --no-experimental-webstorage: Node 25+
  // predefines a global localStorage accessor that returns undefined
  // without --localstorage-file, and being already on globalThis it
  // blocks jsdom's real Storage when vitest populates the environment,
  // so every bare localStorage in a test is undefined. The flag removes
  // Node's accessor and jsdom wins, on Node 22 (CI) and newer alike.
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}"],
    setupFiles: ["./src/test/setup.ts"],
  },
});
