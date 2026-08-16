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
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
  },
});
