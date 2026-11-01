import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { writeFileSync } from "node:fs";
import { resolve } from "node:path";

// web/dist/README.txt is the one committed file in dist (go:embed needs the
// directory to exist). emptyOutDir wipes it on every build, and agents kept
// committing the deletion (c8ce79d, 08137aa), so the build writes it back.
export const DIST_README =
  "This directory holds the built frontend (npm run build in web/).\nIt is embedded into the Go binary via go:embed.\n";
const keepDistReadme = {
  name: "keep-dist-readme",
  closeBundle() {
    writeFileSync(resolve(import.meta.dirname, "dist/README.txt"), DIST_README);
  },
};

// Build artifacts land in web/dist, which the Go binary embeds via go:embed.
// Assets use absolute paths: the binary serves the SPA at /, and relative
// paths broke deep links such as /groups/1 (assets resolved under /groups/).
export default defineConfig({
  plugins: [react(), tailwindcss(), keepDistReadme],
  base: "/",
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // The service worker (src/sw.ts) is a second entry built to exactly
    // dist/sw.js, unhashed: browsers find a worker by a fixed URL, and one
    // served from the root has the whole site as its scope. index.html
    // doesn't link it, so bundle-size.mjs's entry budget doesn't count it.
    // It imports only src/sw/, which the app never does, so it shares no
    // chunk with the page and stays one classic script.
    rolldownOptions: {
      input: { index: resolve(import.meta.dirname, "index.html"), sw: resolve(import.meta.dirname, "src/sw.ts") },
      output: {
        entryFileNames: (chunk) => (chunk.name === "sw" ? "sw.js" : "assets/[name]-[hash].js"),
      },
    },
  },
  server: {
    port: 5173,
    // The manifest and worker come from the Go server (make dev runs it);
    // the worker is whatever web/dist last built.
    proxy: {
      "/manifest.webmanifest": {
        target: "http://localhost:8080",
        changeOrigin: true,
      },
      "/sw.js": {
        target: "http://localhost:8080",
        changeOrigin: true,
      },
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
  // Node's accessor and jsdom wins, on Node 24 (CI) and newer alike.
  test: {
    environment: "jsdom",
    // vm contexts reuse one worker per thread instead of booting jsdom in a
    // fresh fork per file: about a fifth faster, still isolated per file.
    pool: "vmThreads",
    include: ["src/**/*.test.{ts,tsx}"],
    setupFiles: ["./src/test/setup.ts"],
  },
});
