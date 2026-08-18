import { StrictMode } from "react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
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

/**
 * Routes by URL, because Ask now talks to two endpoints: it streams from
 * /api/ask and reads a stored thread from /api/threads/{id}. A stub that
 * answered both the same way would let a component that confuses them pass.
 */
function routedFetch(messages: unknown, frames: string[] = []) {
  const encoder = new TextEncoder();
  const mock = vi.fn(async (url: string) => {
    if (String(url).startsWith("/api/threads/")) {
      return { ok: true, status: 200, json: async () => messages };
    }
    return {
      ok: true,
      status: 200,
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
  });
  vi.stubGlobal("fetch", mock);
  return mock;
}

const storedTurn = {
  id: 9,
  ordinal: 0,
  audience: "dev",
  question: "Wie kommt ein Apple TV an die Datei?",
  answer: "Ueber einen Grant [1].",
  error: "",
  citations: [
    {
      marker: 1,
      repo: "peeq",
      branch: "master",
      path: "backend/internal/playbackgrant/store.go",
      start_line: 3,
      end_line: 40,
    },
  ],
  created_at: "2026-08-17T10:00:00Z",
};

describe("Ask, ein gespeicherter Thread", () => {
  // Rendered the way main.tsx mounts the app. StrictMode runs every effect
  // twice, and the first version of the loader cancelled its own only request
  // in the cleanup between the two runs: the tests passed, the real app came
  // back from a reload with an empty thread.
  const strict = (ui: React.ReactNode) => render(<StrictMode>{ui}</StrictMode>);

  it("stellt einen alten Zug samt Belegen aus dem Protokoll her", async () => {
    routedFetch([storedTurn]);
    strict(<Ask threadId={7} />);

    expect(await screen.findByText(/Ueber einen Grant/)).toBeTruthy();
    expect(screen.getByText(/Wie kommt ein Apple TV an die Datei/)).toBeTruthy();
    expect(screen.getByText(/store\.go:3-40/)).toBeTruthy();
    // A restored turn is finished. A status line would claim something is
    // still running.
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("stellt die Rolle wieder her, mit der die Frage beantwortet wurde", async () => {
    routedFetch([storedTurn]);
    strict(<Ask threadId={7} />);
    expect(await screen.findByText("Developer")).toBeTruthy();
  });

  // Messages() puts the subject inside the WHERE clause and returns an empty
  // list for a thread that is not yours or no longer exists — 200, not 403.
  // Waiting for an error status would leave a dead id in localStorage forever.
  it("gibt eine tote Thread-Nummer zurueck, statt einen leeren Thread zu zeigen", async () => {
    routedFetch([]);
    const onThread = vi.fn();
    strict(<Ask threadId={999} onThread={onThread} />);
    await waitFor(() => expect(onThread).toHaveBeenCalledWith(null));
  });

  it("laedt den eigenen laufenden Thread nicht mitten im Stream neu", async () => {
    // The stream's thread event reports the id back upwards; if that round trip
    // re-triggered the loader, the half-written answer would be replaced by the
    // stored record, which does not have it yet.
    const mock = routedFetch(
      [storedTurn],
      [ev("thread", { thread_id: 42 }), ev("token", { text: "Der Versand laeuft." }), ev("done", {})],
    );
    const user = userEvent.setup();
    const { rerender } = render(<Ask threadId={null} onThread={() => {}} />);
    await user.type(screen.getByLabelText("Frage"), "Wie?");
    await user.click(screen.getByRole("button", { name: "Fragen" }));
    await screen.findByText(/Der Versand laeuft/);

    rerender(<Ask threadId={42} onThread={() => {}} />);
    await waitFor(() =>
      expect(mock.mock.calls.filter((c) => String(c[0]).startsWith("/api/threads/")).length).toBe(0),
    );
    expect(screen.getByText(/Der Versand laeuft/)).toBeTruthy();
  });

  /**
   * Like routedFetch, but the thread load only resolves when the test releases
   * it. Every bug below lives in the window between sending that request and
   * its answer arriving, and a fake that resolves immediately closes exactly
   * that window.
   */
  function slowThreadFetch(messages: unknown, frames: string[] = [], status = 200) {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const encoder = new TextEncoder();
    const mock = vi.fn(async (url: string) => {
      if (String(url).startsWith("/api/threads/")) {
        await gate;
        return { ok: status >= 200 && status < 300, status, json: async () => messages };
      }
      return {
        ok: true,
        status: 200,
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
    });
    vi.stubGlobal("fetch", mock);
    return { mock, release: () => release() };
  }

  it("wirft einen abgewaehlten Thread nicht nachtraeglich auf den Schirm", async () => {
    // Thread waehlen, dann «Neue Frage», bevor die Antwort da ist. Die
    // eintreffende Antwort gehoert zu einem Thread, der nicht mehr offen ist.
    const { release } = slowThreadFetch([storedTurn]);
    const { rerender } = render(<Ask threadId={7} onThread={() => {}} />);
    rerender(<Ask threadId={null} onThread={() => {}} />);
    // Released and then flushed: a plain waitFor can poll before the resolved
    // promise's continuation has run and pass on a component that is about to
    // render the wrong thread.
    release();
    await act(async () => {});
    expect(screen.queryByText(/Ueber einen Grant/)).toBeNull();
  });

  it("ueberschreibt einen laufenden Zug nicht mit dem gespeicherten Protokoll", async () => {
    // Reload auf einen gemerkten Thread, und der Backend ist langsam: die Frage
    // ist schon abgeschickt, wenn das Protokoll eintrifft. Ohne Schutz faellt
    // der laufende Zug weg und die weiteren Token landen in einer fertigen,
    // gespeicherten Antwort.
    const { release } = slowThreadFetch([storedTurn], [
      ev("thread", { thread_id: 7 }),
      ev("token", { text: "Die neue Antwort." }),
      ev("done", {}),
    ]);
    const user = userEvent.setup();
    render(<Ask threadId={7} onThread={() => {}} />);
    await user.type(screen.getByLabelText("Frage"), "Und jetzt?");
    await user.click(screen.getByRole("button", { name: "Fragen" }));
    await screen.findByText(/Die neue Antwort/);

    release();
    await act(async () => {});
    expect(screen.getByText(/Die neue Antwort/)).toBeTruthy();
    expect(screen.getByText("Und jetzt?")).toBeTruthy();
  });

  it("vergisst den Thread nicht, wenn der Server kurz stolpert", async () => {
    // 503 heisst «gerade nicht», nicht «gibt es nicht». Nur 200 mit leerer
    // Liste heisst, dass der Thread einem nicht gehoert oder weg ist.
    const { release } = slowThreadFetch(null, [], 503);
    const onThread = vi.fn();
    render(<Ask threadId={7} onThread={onThread} />);
    release();
    await act(async () => {});
    expect(onThread).not.toHaveBeenCalledWith(null);
  });

  it("zeigt nach einem gescheiterten Wechsel nicht den alten Thread weiter", async () => {
    // Von Thread 7 auf 8 wechseln und das Netz faellt aus. Sieht man weiter
    // Thread 7, geht die naechste Frage stillschweigend in Thread 8.
    routedFetch([storedTurn]);
    const { rerender } = render(<Ask threadId={7} onThread={() => {}} />);
    await screen.findByText(/Ueber einen Grant/);

    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new Error("offline");
      }),
    );
    rerender(<Ask threadId={8} onThread={() => {}} />);
    await waitFor(() => expect(screen.queryByText(/Ueber einen Grant/)).toBeNull());
  });

  it("meldet den Thread nach oben, sobald er angelegt ist, und wenn der Zug fertig ist", async () => {
    routedFetch([], [ev("thread", { thread_id: 42 }), ev("token", { text: "So." }), ev("done", {})]);
    const onActivity = vi.fn();
    const user = userEvent.setup();
    render(<Ask threadId={null} onActivity={onActivity} />);
    await user.type(screen.getByLabelText("Frage"), "Wie?");
    await user.click(screen.getByRole("button", { name: "Fragen" }));

    // Twice: once so the placeholder title appears immediately, once at the end
    // so the model-written title replaces it.
    await waitFor(() => expect(onActivity).toHaveBeenCalledTimes(2));
  });
});
