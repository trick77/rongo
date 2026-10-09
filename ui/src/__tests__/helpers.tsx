/**
 * What the component tests share: a strict-mode render, a fake fetch that
 * streams SSE frames one chunk at a time, a fake that serves one JSON
 * answer, and the two moves every Ask test makes. Not a suite, because the
 * name is not *.test.*; not in coverage, because vite.config.ts excludes
 * the directory.
 */
import { StrictMode, type ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import Ask from "../Ask";
import { languages } from "../turns";

/** One SSE frame as the server writes it. */
export const ev = (name: string, data: unknown) => `event: ${name}\ndata: ${JSON.stringify(data)}\n\n`;

export const strict = (ui: ReactNode) => render(<StrictMode>{ui}</StrictMode>);

/**
 * Streams the given SSE frames one chunk at a time. A fake that returned the
 * whole body at once would let a component that waits for the end pass, and
 * the symptom in the real app is an answer that appears only when it is
 * finished. threads, when given, is what the thread list request gets
 * instead of the stream.
 */
export function streamFrames(frames: string[], { status = 200, threads }: { status?: number; threads?: unknown } = {}) {
  const encoder = new TextEncoder();
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      if (threads !== undefined && String(url).startsWith("/api/threads/")) {
        return { ok: true, status: 200, json: async () => threads };
      }
      return {
        ok: status >= 200 && status < 300,
        status,
        body: {
          getReader() {
            let i = 0;
            return {
              async read() {
                if (i >= frames.length) return { done: true, value: undefined };
                return { done: false, value: encoder.encode(frames[i++]) };
              },
            };
          },
        },
      };
    }),
  );
}

/** Serves one answer to every request, with the body as JSON or as text. */
export function serve(status: number, body: unknown) {
  const fetchMock = vi.fn(async () => ({
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
    text: async () => (typeof body === "string" ? body : JSON.stringify(body)),
  }));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

/** The language list is a listbox, not a native select: the pill opens it
 * and the row is clicked, as the reader does it. */
export async function pickLanguage(user: ReturnType<typeof userEvent.setup>, code: string) {
  const name = languages.find((l) => l.code === code)?.name ?? code;
  await user.click(screen.getByRole("combobox", { name: "Answer language" }));
  await user.click(screen.getByRole("option", { name }));
}

/** Renders Ask, picks a language when asked to, types the question and
 * presses Ask. */
export async function ask(text: string, language?: string) {
  const user = userEvent.setup();
  strict(<Ask />);
  if (language) await pickLanguage(user, language);
  await user.type(screen.getByLabelText("Question"), text);
  await user.click(screen.getByRole("button", { name: "Ask" }));
  return user;
}
