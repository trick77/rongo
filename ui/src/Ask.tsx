import { useRef, useState } from "react";

type Citation = {
  marker: number;
  repo: string;
  branch: string;
  path: string;
  start_line: number;
  end_line: number;
};

type Turn = {
  question: string;
  audience: "ba" | "dev";
  text: string;
  citations: Citation[];
  status: string;
  error: string;
  done: boolean;
};

/**
 * Parses an SSE body incrementally. The whole point of streaming is that the
 * reader sees the first sentence before the last one exists, so this consumes
 * whatever has arrived and keeps the remainder for the next chunk.
 */
function drain(buffer: string, onEvent: (name: string, data: string) => void): string {
  const blocks = buffer.split("\n\n");
  const rest = blocks.pop() ?? "";
  for (const block of blocks) {
    let name = "";
    let data = "";
    for (const line of block.split("\n")) {
      if (line.startsWith("event: ")) name = line.slice(7);
      else if (line.startsWith("data: ")) data = line.slice(6);
    }
    if (name) onEvent(name, data);
  }
  return rest;
}

function forgeLine(c: Citation): string {
  return `${c.repo} · ${c.path}:${c.start_line}-${c.end_line} (${c.branch})`;
}

/** Chevron, rotating 90 degrees on open. No triangle, no plus/minus. */
function Chevron() {
  return (
    <svg
      className="chev inline-block h-3 w-3 transition-transform"
      viewBox="0 0 12 12"
      aria-hidden="true"
    >
      <path
        d="M4.5 2.5 L8 6 L4.5 9.5"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

export default function Ask() {
  const [question, setQuestion] = useState("");
  const [audience, setAudience] = useState<"ba" | "dev">("ba");
  const [turns, setTurns] = useState<Turn[]>([]);
  const [busy, setBusy] = useState(false);
  const threadId = useRef<number | null>(null);

  function patchLast(patch: (t: Turn) => Turn) {
    setTurns((prev) => prev.map((t, i) => (i === prev.length - 1 ? patch(t) : t)));
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    const q = question.trim();
    if (!q || busy) return;

    setTurns((prev) => [
      ...prev,
      { question: q, audience, text: "", citations: [], status: "", error: "", done: false },
    ]);
    setQuestion("");
    setBusy(true);

    try {
      const res = await fetch("/api/ask", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ question: q, audience, thread_id: threadId.current ?? 0 }),
      });
      if (!res.ok || !res.body) {
        patchLast((t) => ({ ...t, error: `Der Server antwortete mit ${res.status}.`, done: true }));
        return;
      }
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        buffer = drain(buffer, (name, data) => {
          const payload = data ? JSON.parse(data) : {};
          if (name === "thread") threadId.current = payload.thread_id;
          else if (name === "status") patchLast((t) => ({ ...t, status: payload.step }));
          else if (name === "token") patchLast((t) => ({ ...t, text: t.text + payload.text }));
          else if (name === "citations") patchLast((t) => ({ ...t, citations: payload ?? [] }));
          else if (name === "error") patchLast((t) => ({ ...t, error: payload.message, done: true }));
          else if (name === "done") patchLast((t) => ({ ...t, done: true, status: "" }));
        });
      }
    } catch {
      patchLast((t) => ({ ...t, error: "Die Verbindung ist abgebrochen.", done: true }));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div>
      {turns.map((turn, i) => (
        <article key={i} className="mb-8 border-b border-[var(--color-hairline)] pb-6">
          <p className="font-medium">{turn.question}</p>
          <p className="mt-1 text-xs text-[var(--color-ink-faint)]">
            {turn.audience === "ba" ? "Business Analyst" : "Developer"}
          </p>

          {turn.status && !turn.done && (
            <p role="status" aria-live="polite" className="mt-3 text-sm text-[var(--color-ink-soft)]">
              {turn.status}…
            </p>
          )}

          {turn.text && <p className="mt-3 whitespace-pre-wrap">{turn.text}</p>}

          {turn.error && (
            <p role="alert" className="mt-3 text-[var(--color-ochre)]">
              {turn.error}
            </p>
          )}

          {turn.citations.length > 0 && (
            <details className="mt-4 text-sm">
              <summary className="cursor-pointer text-[var(--color-ink-soft)]">
                <Chevron /> Woher weiss rongo das?{" "}
                <span className="text-[var(--color-ink-faint)]">
                  {turn.citations.length} Belege
                </span>
              </summary>
              <ul className="mt-2 space-y-1">
                {turn.citations.map((c) => (
                  <li key={c.marker}>
                    <sup>{c.marker}</sup> <code>{forgeLine(c)}</code>
                  </li>
                ))}
              </ul>
            </details>
          )}
        </article>
      ))}

      <form onSubmit={submit} className="sticky bottom-0 bg-[var(--color-ground)] pt-2">
        <textarea
          value={question}
          onChange={(e) => setQuestion(e.target.value)}
          rows={3}
          aria-label="Frage"
          placeholder="Wie wird die Teaser-Mail verschickt?"
          className="w-full rounded border border-[var(--color-hairline)] bg-[var(--color-surface)] p-3"
        />
        <div className="mt-2 flex items-center gap-3">
          <fieldset className="flex gap-1" aria-label="Rolle">
            {(["ba", "dev"] as const).map((role) => (
              <button
                key={role}
                type="button"
                aria-pressed={audience === role}
                onClick={() => setAudience(role)}
                className={
                  "rounded border px-3 py-1 text-sm " +
                  (audience === role
                    ? "border-[var(--color-accent)] text-[var(--color-accent)]"
                    : "border-[var(--color-hairline)] text-[var(--color-ink-soft)]")
                }
              >
                {role.toUpperCase()}
              </button>
            ))}
          </fieldset>
          <button
            type="submit"
            disabled={busy}
            className="rounded bg-[var(--color-accent)] px-4 py-1 text-sm text-white disabled:opacity-50"
          >
            Fragen
          </button>
        </div>
      </form>
    </div>
  );
}
