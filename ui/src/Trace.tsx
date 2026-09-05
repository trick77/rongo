import { useEffect, useState } from "react";

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
 * The timeline is always expanded and grows as the steps arrive: progress is
 * something the reader watches, not something they open. Every step is a node
 * on one continuous line with the time it took, the running one carries the
 * spinner, and the last row is the state the turn closed in.
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
    <div role="status" aria-live="polite" className="mt-4">
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
        {closing && (
          <li>
            <span className={"node node-end " + closing.node} aria-hidden="true" />
            <span className={"leading-7 font-medium " + closing.text}>{closing.label}</span>
            <time className="font-mono text-[11.5px] leading-7 tabular-nums text-faint">
              {seconds(closedAt - startedAt)}
            </time>
          </li>
        )}
      </ol>
    </div>
  );
}
