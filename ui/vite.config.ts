import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  // The SPA is built straight into the Go binary's embed directory, which
  // also holds a tracked .gitkeep (see Makefile fe-build comment) so
  // `//go:embed all:dist` always has something to embed. emptyOutDir:true
  // would delete that tracked file on every build; the Makefile's fe-build
  // target does the stale-asset cleanup instead, so leave this false.
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
    setupFiles: ["./src/test-setup.ts"],
    coverage: {
      provider: "v8",
      // json-summary is what hack/coverage-gate.sh reads (the project floor);
      // lcov is what diff-cover reads in hack/patch-coverage.sh (patch
      // coverage); text-summary is for humans reading the CI log.
      reporter: ["text-summary", "json-summary", "lcov"],
      reportsDirectory: "../coverage/ui",
      // Without an explicit include, the v8 provider only instruments files that
      // some test actually imports. A source file nobody tests then contributes
      // nothing to either side of the ratio and is silently invisible, which
      // inflates the reported percentage and lets the floor be met while whole
      // modules go untested.
      include: ["src/**/*.{ts,tsx}"],
      // App bootstrap and type-only declarations: nothing here is behaviour a
      // test could meaningfully assert. Test files and `setupFiles` are not
      // listed because vitest appends both to coverage.exclude itself, and
      // that cannot be overridden.
      exclude: ["src/main.tsx", "src/**/*.d.ts"],
    },
  },
});
