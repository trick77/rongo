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

/** What a step found, as the backend reports it once the step is done. */
export type StepDetail = Record<string, unknown>;

/** One status event, with the moment it arrived, and what the step found. */
export type Step = { step: string; at: number; detail?: StepDetail };

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

/**
 * The routing rung, as the reader should hear it. The backend's names are the
 * ladder's own (route.go); a reader is told what the rung looked at, never
 * its identifier.
 */
const rungSentences: Record<string, Record<string, string>> = {
  answer: {
    named_repos: "Answering directly: the question named the project, so there is nothing to choose.",
    all_repos: "Answering across every project, as the question asked.",
    repo_deps: "Answering directly: the matching repositories depend on each other, so they are one mechanism.",
    margin: "Answering directly: one module matched far ahead of the rest.",
    judge: "Answering directly: the matches are parts of one mechanism.",
    role: "Answering directly: the options could only be told apart by code.",
  },
  ask: {
    repository: "Asking back: the matches span several projects and the question named none.",
    judge: "Asking back: the matches are independent alternatives.",
    margin: "Asking back: no module matched clearly ahead of the others.",
  },
  too_broad: {
    too_broad: "Asking for a narrower question: more projects matched than a card can offer.",
  },
};

function rungSentence(decision: string, rung: string): string {
  return rungSentences[decision]?.[rung] ?? (decision === "ask" ? "Asking back." : "Answering directly.");
}

function seconds(ms: number): string {
  return (Math.max(ms, 0) / 1000).toFixed(1) + "s";
}

const asStrings = (v: unknown): string[] =>
  Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
const asNumber = (v: unknown): number | null => (typeof v === "number" ? v : null);

function Chips({ values, dim }: { values: string[]; dim?: boolean }) {
  return (
    <>
      {values.map((v) => (
        <span key={v} className={"trace-chip" + (dim ? " trace-chip-dim" : "")}>
          {v}
        </span>
      ))}
    </>
  );
}

function tokens(n: number): string {
  return n >= 1000 ? (n / 1000).toFixed(1) + "k" : String(n);
}

/**
 * What a step found, drawn under its label. Every fact here is one the backend
 * already held for the step; nothing is a claim about the answer. A step
 * without a detail draws no row, which is every turn stored before the
 * detail existed.
 */
function Detail({ step, detail }: { step: string; detail: StepDetail }) {
  switch (step) {
    case "understanding": {
      const terms = asStrings(detail.terms);
      const code = asStrings(detail.code_terms);
      const repos = asStrings(detail.repos);
      const unknown = asStrings(detail.unknown_repos);
      const outside = asStrings(detail.outside_repos);
      let scope: string;
      if (repos.length > 0) {
        scope = repos.join(", ") + (detail.pinned ? ", the thread's scope" : ", named by the question");
      } else if (detail.all_repos) {
        scope = "every indexed project, as the question asked";
      } else {
        scope = "every indexed project (none named)";
      }
      return (
        <div className="trace-detail">
          {terms.length > 0 && (
            <>
              <span className="trace-k">Looking for</span> <Chips values={terms} />
            </>
          )}
          {code.length > 0 && (
            <>
              {" "}
              <span className="trace-k">in the code as</span> <Chips values={code} />
            </>
          )}
          <br />
          <span className="trace-k">Scope</span> {scope}
          {unknown.length > 0 && <>; not indexed: {unknown.join(", ")}</>}
          {outside.length > 0 && <>; outside this thread: {outside.join(", ")}</>}
        </div>
      );
    }
    case "searching": {
      const hits = asNumber(detail.hits) ?? 0;
      const perRepo = (detail.per_repo ?? {}) as Record<string, number>;
      const best = (detail.best ?? null) as { repo?: string; path?: string; lanes?: string[] } | null;
      const lanes = asStrings(best?.lanes).map((l) => l.replace("keyword:", "keyword ").replace(/^semantic:\d+$/, "semantic"));
      return (
        <div className="trace-detail">
          {hits} {hits === 1 ? "hit" : "hits"}
          {Object.keys(perRepo).length > 0 && (
            <>
              {" · "}
              <Chips dim values={Object.entries(perRepo).map(([r, n]) => `${r} ${n}`)} />
            </>
          )}
          {best?.path && (
            <>
              {" · best "}
              <span className="trace-chip">
                {best.repo}/{best.path}
              </span>
              {lanes.length > 0 && (
                <>
                  {" "}
                  <span className="trace-k">found by</span> <Chips dim values={[...new Set(lanes)]} />
                </>
              )}
            </>
          )}
        </div>
      );
    }
    case "routing": {
      const decision = String(detail.decision ?? "answer");
      const rung = String(detail.rung ?? "");
      const candidates = asStrings(detail.candidates);
      return (
        <div className="trace-detail">
          {rungSentence(decision, rung)}
          {decision !== "answer" && candidates.length > 0 && (
            <>
              {" "}
              <Chips values={candidates} />
            </>
          )}
          {rung !== "judge" && rung !== "role" && decision !== "answer" && <> · decided by rule, no model</>}
        </div>
      );
    }
    case "gathering": {
      const hits = asNumber(detail.hits) ?? 0;
      const refs = asNumber(detail.references) ?? 0;
      const crossings = asNumber(detail.crossings) ?? 0;
      const sources = asNumber(detail.sources) ?? 0;
      const repos = asNumber(detail.repos) ?? 0;
      const used = asNumber(detail.tokens);
      const budget = asNumber(detail.budget);
      const crossed = (Array.isArray(detail.crossed) ? detail.crossed : []) as { from?: string; to?: string; via?: string }[];
      return (
        <div className="trace-detail">
          {hits} hits
          {refs > 0 && (
            <>
              {" → "}
              <span className="trace-chip">+{refs} referenced</span>
            </>
          )}
          {crossings > 0 && (
            <>
              {" "}
              <span className="trace-chip trace-chip-edge">+{crossings} across a boundary</span>
            </>
          )}
          {" · "}
          {sources} sources in {repos} {repos === 1 ? "repository" : "repositories"}
          {used !== null && budget !== null && (
            <>
              {" · "}
              {tokens(used)} of {tokens(budget)} tokens
            </>
          )}
          {crossed.map((c, i) => (
            <span key={i}>
              <br />
              <span className="trace-k">Crossed</span> {c.from} → {c.to} <span className="trace-k">on the</span> {c.via}
            </span>
          ))}
        </div>
      );
    }
    case "writing": {
      const inTok = asNumber(detail.prompt_tokens);
      const outTok = asNumber(detail.completion_tokens);
      const cited = asNumber(detail.cited);
      const sources = asNumber(detail.sources);
      return (
        <div className="trace-detail">
          {inTok !== null && <>{tokens(inTok)} tokens in</>}
          {outTok !== null && <>{inTok !== null ? " · " : ""}{tokens(outTok)} tokens out</>}
          {cited !== null && sources !== null && (
            <>
              {" · "}
              {cited} of {sources} sources cited
            </>
          )}
        </div>
      );
    }
    default:
      return null;
  }
}

/**
 * The timeline is expanded and grows as the steps arrive for as long as the turn
 * runs: progress is something the reader watches, and while it is moving there is
 * no toggle on screen at all. Every step is a node on one continuous line with the
 * time it took, and the running one carries the spinner. A step that has reported
 * what it found carries that under its label.
 *
 * The closing row is the state the turn ended in, and it is also the toggle: when
 * the turn closes, the steps roll up behind it and the finished trace is one row
 * carrying its node and the total. A chevron opens it again, and once the reader
 * has opened it nothing closes it behind their back - the roll-up fires on the
 * running -> closed transition, once.
 *
 * `role="status"` and `aria-live="polite"` while the turn is this session's, so
 * progress is read out as it happens and the row it closes on is announced. A
 * turn read back out of the record gets neither: nothing there is happening,
 * and a thread of ten stored turns would be ten live regions announcing
 * themselves as the page loads.
 */
export default function Trace({
  steps,
  state,
  startedAt,
  endedAt = null,
  live = true,
}: {
  steps: Step[];
  state: TraceState;
  /** When the turn was sent; the total on the closing row counts from here. */
  startedAt: number;
  /** When the turn closed, or null while it runs. */
  endedAt?: number | null;
  /** Whether this turn is one THIS session watched, rather than a record of
   * one. Only a turn being watched is announced. */
  live?: boolean;
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
    <div role={live ? "status" : undefined} aria-live={live ? "polite" : undefined} className="trace mt-4">
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
                  {s.detail && <Detail step={s.step} detail={s.detail} />}
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
