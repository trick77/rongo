import { useState } from "react";
import { Chevron } from "./Ask";

/**
 * The activity trace has THREE states, not two: a turn that ended by asking
 * a clarification closes on an ochre "waiting" node, never the check — a
 * person is still being waited on, and `!active && !streaming` would call
 * that "done".
 */
export type TraceState = "running" | "done" | "waiting";

const doneLabel = "Fertig";
const waitingLabel = "Wartet auf Auswahl";

/**
 * Collapsed, the trace is one line: the running step, or the closing word
 * once the turn has ended. Expanded, every step becomes a node on one
 * continuous line, ending with the state the turn actually closed in.
 *
 * `role="status"` and `aria-live="polite"` so progress is read out as it
 * happens, not only once the panel is opened.
 */
export default function Trace({ steps, state }: { steps: string[]; state: TraceState }) {
  const [open, setOpen] = useState(false);
  const current = steps[steps.length - 1] ?? "";
  const closingLabel = state === "waiting" ? waitingLabel : state === "done" ? doneLabel : `${current}…`;

  return (
    <div role="status" aria-live="polite" className="mt-3 text-sm text-[var(--color-ink-soft)]">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="flex items-center gap-2"
      >
        <Chevron open={open} />
        <span>{open ? "Ablauf" : closingLabel}</span>
      </button>

      {open && (
        <ol className="mt-2 flex flex-wrap items-center gap-3">
          {steps.map((s, i) => (
            <li key={i} className="flex items-center gap-2">
              <span className="h-2 w-2 rounded-full bg-[var(--color-accent)]" aria-hidden="true" />
              {s}
            </li>
          ))}
          <li className="flex items-center gap-2">
            <span
              className={
                "h-2 w-2 rounded-full " +
                (state === "waiting"
                  ? "bg-[var(--color-ochre)]"
                  : state === "done"
                    ? "bg-[var(--color-accent)]"
                    : "border border-[var(--color-hairline)]")
              }
              aria-hidden="true"
            />
            {state === "waiting" ? waitingLabel : state === "done" ? doneLabel : "…"}
          </li>
        </ol>
      )}
    </div>
  );
}
