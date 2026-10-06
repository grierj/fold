import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Build artifacts land in web/dist, which the Go binary embeds via go:embed.
// Assets use relative paths so the bundle works when served at / by the binary.
export default defineConfig({
  plugins: [react()],
  base: "./",
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
    },
  },
});
