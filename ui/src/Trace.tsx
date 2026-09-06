import { useEffect, useRef, useState } from "react";
import { Chevron } from "./icons";

/**
 * The activity trace has more than two states: a turn that ended by asking
 * a clarification closes on an ochre "waiting" node, never the check — a
 * person is still being waited on. Once that person has chosen, the node
 * loses its colour ("decided"): ochre means "your move", and the move has
 * been made. A turn that broke closes on the failure node.
 */
export type TraceState = "running" | "done" | "waiting" | "decided" | "failed";

/** One status event, with the moment it arrived. */
export type Step = { step: string; at: number };

const doneLabel = "Done";
const waitingLabel = "Waiting for a choice";
const decidedLabel = "Asked back, choice made";
const failedLabel = "The turn failed";

/**
 * The backend reports a step as one word. The label is what a person reads,
 * and an unknown word is shown as it came rather than hidden.
 */
const stepLabels: Record<string, string> = {
  understanding: "Understanding the question",
  searching: "Searching the index",
  routing: "Deciding whether to ask back",
  gathering: "Reading the code",
  // Two steps, not one: "answering" is reported before the model is called, so
  // for as long as it reasons nothing is being written yet. Calling that stretch
  // "writing" was a claim the empty answer pane contradicted. The backend emits
  // "writing" on the first token.
  answering: "Thinking about the answer",
  writing: "Writing the answer",
  // After the answer, not during it: the questions are written from it.
  suggesting: "Suggesting follow-ups",
};

export function stepLabel(step: string): string {
  return stepLabels[step] ?? step;
}

function seconds(ms: number): string {
  return (Math.max(ms, 0) / 1000).toFixed(1) + "s";
}

/**
 * The timeline is expanded and grows as the steps arrive for as long as the turn
 * runs: progress is something the reader watches, and while it is moving there is
 * no toggle on screen at all. Every step is a node on one continuous line with the
 * time it took, and the running one carries the spinner.
 *
 * The closing row is the state the turn ended in, and it is also the toggle: when
 * the turn closes, the steps roll up behind it and the finished trace is one row
 * carrying its node and the total. A chevron opens it again, and once the reader
 * has opened it nothing closes it behind their back - the roll-up fires on the
 * running -> closed transition, once.
 *
 * `role="status"` and `aria-live="polite"` so progress is read out as it
 * happens.
 */
export default function Trace({
  steps,
  state,
  startedAt,
  endedAt = null,
}: {
  steps: Step[];
  state: TraceState;
  /** When the turn was sent; the total on the closing row counts from here. */
  startedAt: number;
  /** When the turn closed, or null while it runs. */
  endedAt?: number | null;
}) {
  // A running step's duration ticks; once the turn has closed nothing moves.
  const [now, setNow] = useState(() => Date.now());
  // Open only while the turn is still running. A trace that mounts already
  // closed has no roll-up to wait for: a superseded attempt reopened with Show
  // remounts finished, and so does a turn read back out of the record.
  const [open, setOpen] = useState(state === "running");
  // On the transition, never on every render: a reader who opened a finished
  // trace keeps it open, and a later state change (waiting -> decided) does not
  // shut it under them. Same ref pattern as Clarify and Narrow.
  const wasRunning = useRef(state === "running");
  useEffect(() => {
    if (wasRunning.current && state !== "running") setOpen(false);
    wasRunning.current = state === "running";
  }, [state]);
  useEffect(() => {
    if (state !== "running") return;
    const id = setInterval(() => setNow(Date.now()), 500);
    return () => clearInterval(id);
  }, [state]);

  const closedAt = endedAt ?? now;
  const closing =
    state === "done"
      ? { label: doneLabel, node: "node-done", text: "text-ink" }
      : state === "waiting"
        ? { label: waitingLabel, node: "node-ochre", text: "text-ochre" }
        : state === "decided"
          ? { label: decidedLabel, node: "node-decided", text: "text-muted" }
          : state === "failed"
            ? { label: failedLabel, node: "node-fail", text: "text-accent-strong" }
            : null;

  return (
    <div role="status" aria-live="polite" className="trace mt-4">
      <div
        className={"trace-steps" + (open ? " trace-steps-open" : "")}
        aria-hidden={open ? undefined : true}
      >
        <div className="trace-steps-inner">
          <ol className="steps text-sm text-muted">
            {steps.map((s, i) => {
              const last = i === steps.length - 1;
              const running = last && state === "running";
              const until = last ? closedAt : steps[i + 1].at;
              return (
                <li key={i}>
                  <span className={"node " + (running ? "node-now" : "")} aria-hidden="true" />
                  <span className={"leading-7 " + (running ? "font-medium text-ink" : "")}>{stepLabel(s.step)}</span>
                  <time className="font-mono text-[11.5px] leading-7 tabular-nums text-faint">
                    {seconds(until - s.at)}
                  </time>
                </li>
              );
            })}
          </ol>
        </div>
      </div>
      {closing && (
        <ol className="steps text-sm text-muted">
          <li>
            <span className={"node node-end " + closing.node} aria-hidden="true" />
            <button
              type="button"
              aria-expanded={open}
              onClick={() => setOpen((v) => !v)}
              className="flex items-center gap-1.5 text-left"
            >
              <span className={"leading-7 font-medium " + closing.text}>{closing.label}</span>
              {/* Up, not down: the steps are above this row. And faint, the
                  weight of the copy control - the label is what carries the row. */}
              <span className="text-faint transition-colors">
                <Chevron open={open} up />
              </span>
            </button>
            <time className="font-mono text-[11.5px] leading-7 tabular-nums text-faint">
              {seconds(closedAt - startedAt)}
            </time>
          </li>
        </ol>
      )}
    </div>
  );
}
