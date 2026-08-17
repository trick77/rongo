import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// Cleanup after each test.
afterEach(() => {
  cleanup();
});

// jsdom hands out an empty object for localStorage here, not a Storage. The
// open thread survives a reload through it, so a test asserting that has to run
// against something that actually stores — otherwise it would pass by never
// reading back what it wrote.
if (typeof localStorage.getItem !== "function") {
  const cell = new Map<string, string>();
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: {
      getItem: (k: string) => (cell.has(k) ? cell.get(k)! : null),
      setItem: (k: string, v: string) => void cell.set(k, String(v)),
      removeItem: (k: string) => void cell.delete(k),
      clear: () => cell.clear(),
      key: (i: number) => [...cell.keys()][i] ?? null,
      get length() {
        return cell.size;
      },
    },
  });
}
