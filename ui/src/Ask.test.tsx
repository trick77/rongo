import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Ask from "./Ask";

/**
 * Streams the given SSE frames one chunk at a time. A fake that returned the
 * whole body at once would let a component that waits for the end pass, and the
 * symptom in the real app is an answer that appears only when it is finished.
 */
function streamFrames(frames: string[], status = 200) {
  const encoder = new TextEncoder();
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({
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
    })),
  );
}

const ev = (name: string, data: unknown) => `event: ${name}\ndata: ${JSON.stringify(data)}\n\n`;

afterEach(() => vi.unstubAllGlobals());

async function ask(text: string) {
  const user = userEvent.setup();
  render(<Ask />);
  await user.type(screen.getByLabelText("Frage"), text);
  await user.click(screen.getByRole("button", { name: "Fragen" }));
  return user;
}

describe("Ask", () => {
  it("zeigt die Antwort waehrend sie eintrifft, nicht erst am Ende", async () => {
    streamFrames([
      ev("thread", { thread_id: 1, title: "x" }),
      ev("token", { text: "Der Versand " }),
      ev("token", { text: "laeuft ueber einen Job [1]." }),
      ev("citations", [
        { marker: 1, repo: "peeq", branch: "master", path: "a.go", start_line: 1, end_line: 9 },
      ]),
      ev("done", { message_id: 1 }),
    ]);

    await ask("Wie laeuft der Versand?");

    await screen.findByText(/Der Versand laeuft ueber einen Job/);
  });

  it("zeigt den laufenden Schritt, solange nichts fertig ist", async () => {
    streamFrames([ev("thread", { thread_id: 1 }), ev("status", { step: "sammeln" })]);

    await ask("Wie?");

    await waitFor(() => {
      expect(screen.getByRole("status").textContent).toContain("sammeln");
    });
  });

  it("fuehrt die Belege auf, mit Branch — ohne ihn geht ein Forge-Link ins Leere", async () => {
    streamFrames([
      ev("thread", { thread_id: 1 }),
      ev("token", { text: "So ist es [1]." }),
      ev("citations", [
        {
          marker: 1,
          repo: "peeq",
          branch: "release-2024.3",
          path: "backend/internal/playbackgrant/store.go",
          start_line: 3,
          end_line: 40,
        },
      ]),
      ev("done", {}),
    ]);

    await ask("Wie?");

    const evidence = await screen.findByText(/Woher weiss rongo das/);
    expect(evidence).toBeTruthy();
    expect(screen.getByText(/release-2024\.3/)).toBeTruthy();
    expect(screen.getByText(/store\.go:3-40/)).toBeTruthy();
  });

  it("zeigt einen Fehler als Fehler, nicht als leere Antwort", async () => {
    streamFrames([ev("thread", { thread_id: 1 }), ev("error", { message: "Der Zug ist fehlgeschlagen." })]);

    await ask("Wie?");

    await waitFor(() => {
      expect(screen.getByRole("alert").textContent).toContain("fehlgeschlagen");
    });
  });

  it("schickt die gewaehlte Rolle mit", async () => {
    streamFrames([ev("thread", { thread_id: 1 }), ev("done", {})]);
    const user = userEvent.setup();
    render(<Ask />);
    await user.click(screen.getByRole("button", { name: "DEV" }));
    await user.type(screen.getByLabelText("Frage"), "Wie?");
    await user.click(screen.getByRole("button", { name: "Fragen" }));

    await waitFor(() => {
      const body = JSON.parse((globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][1].body);
      expect(body.audience).toBe("dev");
    });
  });

  it("haengt die zweite Frage an denselben Thread", async () => {
    // The thread is a record: a follow-up continues it rather than starting a
    // second conversation about the same subject.
    streamFrames([ev("thread", { thread_id: 42 }), ev("done", {})]);
    const user = await ask("Erste Frage?");

    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalledTimes(1));
    streamFrames([ev("thread", { thread_id: 42 }), ev("done", {})]);
    await user.type(screen.getByLabelText("Frage"), "Und dann?");
    await user.click(screen.getByRole("button", { name: "Fragen" }));

    await waitFor(() => {
      const body = JSON.parse((globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0][1].body);
      expect(body.thread_id).toBe(42);
    });
  });
});
