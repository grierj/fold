import { defineConfig } from "vite";
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
});
