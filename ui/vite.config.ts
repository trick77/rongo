import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  // The SPA is built straight into the Go binary's embed directory, which
  // also holds a tracked .gitkeep (see Makefile fe-build comment) so
  // `//go:embed all:dist` always has something to embed. emptyOutDir:true
  // would delete that tracked file on every build; ship check (ui.embed) and
  // make fe-build do the stale-asset cleanup instead, so leave this false.
  build: { outDir: "../backend/web/dist", emptyOutDir: false },
  server: {
    host: "127.0.0.1",
    proxy: { "/api": "http://127.0.0.1:8080" },
    // The diagram corpus lives in the backend's testdata and both ends read
    // it (src/corpus.test.ts): a spec the backend normalises and this end
    // will not draw is a defect only a shared corpus catches. Reading above
    // the UI root is refused by default, so the repo root is allowed.
    fs: { allow: [".."] },
  },
  test: {
    environment: "jsdom",
    // Globals let Testing Library register its own cleanup after each test.
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    coverage: {
      provider: "v8",
      // lcov feeds ship's UI floor and patch gate; text-summary is for humans
      // reading the log.
      reporter: ["text-summary", "lcov"],
      reportsDirectory: "../coverage/ui",
      // Without an explicit include, the v8 provider only instruments files that
      // some test actually imports. A source file nobody tests then contributes
      // nothing to either side of the ratio and is silently invisible, which
      // inflates the reported percentage and lets the floor be met while whole
      // modules go untested.
      include: ["src/**/*.{ts,tsx}"],
      // App bootstrap, type-only declarations and the shared test helpers:
      // nothing here is behaviour a test could meaningfully assert. Test files
      // and `setupFiles` are not listed because vitest appends both to
      // coverage.exclude itself, and that cannot be overridden.
      exclude: ["src/main.tsx", "src/**/*.d.ts", "src/__tests__/**"],
    },
  },
});
