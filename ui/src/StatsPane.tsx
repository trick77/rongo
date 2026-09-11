import { useEffect, useState } from "react";
import type { Turn, UsageCall } from "./turns";
import { money } from "./turns";

/* The stats pane: what a turn spent, and what the thread has spent.
 *
 * It opens from either usage pill - the one under an answer lands on the
 * turn, the one beside the thread title lands on the thread - because those
 * are the two questions a reader actually has, and answering them in two
 * different places would mean reading the same figures twice.
 *
 * Every count here is what the endpoint billed, with one exception that is
 * labelled where it is drawn: the prompt split under "what filled the answer
 * call" is measured locally at four characters per token, because the
 * upstream reports one prompt figure and never says which part of it was the
 * rules and which the code. */

/** glossary is one sentence per word a reader should not have to guess at.
 * Written the way the trace writes its details: what happened, not what the
 * component is called. */
const glossary: Record<string, string> = {
  embedding:
    "The question is turned into a vector so the meaning search has something to compare against. One call, a few dozen tokens.",
  lanes:
    "Two searches run over the index: one matches words, one matches meaning. Their results are merged before anything is ranked.",
  hops: "A gathered file pointed at another. That one was fetched too, up to two hops out, until the budget ran out.",
  rerank:
    "Sixty results went to the small model, which put the ones that answer the question first. The list is cut to twenty after that.",
  cached:
    "The endpoint had already read this part of the prompt and did not charge full price for it again. A thread's later turns repeat the prefix of the first, which is when it happens.",
  context:
    "How much of the model's window this call used. The window comes from the price registry; a model it does not list shows no window.",
  budget:
    "What the source walk may spend. When it runs out the walk stops where it is; nothing already gathered is cut short.",
  reasoning: "The part of the reply the model spent thinking rather than writing. It comes out of the same length cap.",
};

/** Info is the ⓘ: click opens one line, click again closes it. */
function Info({ term, open, onToggle }: { term: string; open: boolean; onToggle: () => void }) {
  return (
    <button
      type="button"
      aria-label={`What ${term} means`}
      aria-expanded={open}
      onClick={onToggle}
      className={
        "ml-1.5 inline-flex h-[13px] w-[13px] items-center justify-center rounded-full border align-[1px] font-sans text-[9.5px] leading-none " +
        (open ? "border-elevated-border text-muted" : "border-border text-faint hover:border-elevated-border hover:text-muted")
      }
    >
      i
    </button>
  );
}

/** Said is the line an ⓘ opens. */
function Said({ children }: { children: React.ReactNode }) {
  return (
    <p className="col-span-full my-0.5 mb-2 rounded-ui-sm border border-border border-l-2 border-l-elevated-border bg-bg px-3 py-2 font-sans text-[12.5px] leading-relaxed text-muted">
      {children}
    </p>
  );
}

/** num is a count in the pane's own style: grouped, never abbreviated. The
 * trace abbreviates because it writes sentences; a table does not. */
function num(n: number): string {
  return n.toLocaleString("en-GB");
}

/** ms reads a duration the way a person says it: milliseconds under a
 * second, seconds above. */
function ms(n: number): string {
  return n < 1000 ? `${n} ms` : `${(n / 1000).toFixed(1)} s`;
}

/** detailOf reads one step's detail out of a turn's trace. The trace is the
 * only place the prompt split and the source budget are recorded. */
function detailOf(turn: Turn, step: string): Record<string, unknown> | null {
  for (let i = turn.steps.length - 1; i >= 0; i--) {
    if (turn.steps[i].step === step && turn.steps[i].detail) return turn.steps[i].detail!;
  }
  return null;
}

function int(d: Record<string, unknown> | null, key: string): number | null {
  const v = d?.[key];
  return typeof v === "number" ? v : null;
}

/** counts is what the turn did, split by what it cost: model calls and the
 * embedding call are paid for, the lanes and hops are the work around them.
 * There is no third kind - the client has no tools. */
function counts(turn: Turn) {
  const calls = turn.usage?.calls ?? [];
  const search = detailOf(turn, "searching");
  const gather = detailOf(turn, "gathering");
  const lanes = int(search, "hits") == null ? null : 2;
  const refs = int(gather, "references");
  const crossings = int(gather, "crossings");
  return {
    model: calls.filter((c) => c.step !== "embed").length,
    embedding: calls.filter((c) => c.step === "embed").length,
    lanes,
    hops: refs == null && crossings == null ? null : (refs ?? 0) + (crossings ?? 0),
  };
}

/** Bar is the prompt split: the three parts against each other, not against
 * the window. A window of a million tokens against a prompt of twenty
 * thousand draws a sliver that looks the same on every turn ever answered,
 * which is why the window is a number here and never a bar. */
function Bar({ system, sources, question }: { system: number; sources: number; question: number }) {
  const total = Math.max(system + sources + question, 1);
  const pct = (n: number) => `${(n / total) * 100}%`;
  return (
    <>
      <div className="col-span-full mt-1.5 flex h-2.5 overflow-hidden rounded-ui-sm bg-active">
        <span style={{ width: pct(system) }} className="block h-full bg-faint" />
        <span style={{ width: pct(sources) }} className="block h-full bg-accent-strong" />
        <span style={{ width: pct(question) }} className="block h-full bg-rail" />
      </div>
      <div className="col-span-full mt-2 flex flex-wrap gap-x-3 gap-y-1 font-mono text-[11.5px] text-faint">
        <span>
          <i className="mr-1.5 inline-block h-2 w-2 rounded-[2px] bg-faint align-[-1px]" />
          system prompt <b className="font-normal text-muted">{num(system)}</b>
        </span>
        <span>
          <i className="mr-1.5 inline-block h-2 w-2 rounded-[2px] bg-accent-strong align-[-1px]" />
          gathered sources <b className="font-normal text-muted">{num(sources)}</b>
        </span>
        <span>
          <i className="mr-1.5 inline-block h-2 w-2 rounded-[2px] bg-rail align-[-1px]" />
          question <b className="font-normal text-muted">{num(question)}</b>
        </span>
      </div>
    </>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="mt-5">
      <h3 className="mb-2 text-[11px] font-medium tracking-[.1em] text-faint uppercase">{title}</h3>
      <div className="grid grid-cols-[1fr_auto] items-center gap-x-2.5 font-mono text-xs">{children}</div>
    </div>
  );
}

/** Row is one figure with its label. */
function Row({ label, value }: { label: React.ReactNode; value: React.ReactNode }) {
  return (
    <>
      <div className="py-1 text-ink-dim">{label}</div>
      <div className="py-1 text-right text-muted tabular-nums">{value}</div>
    </>
  );
}

function Kpi({ value, label }: { value: string; label: string }) {
  return (
    <div className="rounded-ui-sm border border-border bg-bg px-3 py-2.5">
      <div className="font-mono text-[17px] text-ink-dim tabular-nums">{value}</div>
      <div className="mt-0.5 text-[11.5px] text-faint">{label}</div>
    </div>
  );
}

/** TurnStats is the "This turn" side. */
function TurnStats({ turn, said, toggle }: { turn: Turn; said: string | null; toggle: (t: string) => void }) {
  const u = turn.usage;
  if (!u) return <p className="mt-4 text-[13px] text-muted">This turn has no usage on record.</p>;
  const c = counts(turn);
  const writing = detailOf(turn, "writing");
  const gather = detailOf(turn, "gathering");
  const system = int(writing, "prompt_system");
  const sources = int(writing, "prompt_sources");
  const question = int(writing, "prompt_question");
  const answer = u.calls.find((x) => x.step === "answer");
  const window = answer?.context_tokens ?? 0;
  const anyMs = u.calls.some((x) => x.ms != null);
  const anyCached = u.calls.some((x) => x.cached_tokens != null);
  const totalMs = u.calls.reduce((n, x) => n + (x.ms ?? 0), 0);
  const thought = u.calls.reduce((n, x) => n + (x.reasoning_tokens ?? 0), 0);

  return (
    <>
      <div className="grid grid-cols-2 gap-2">
        <Kpi value={num(u.total_tokens)} label="tokens" />
        {u.cost_usd != null && <Kpi value={money(u.cost_usd)} label="at list price" />}
        {/* Added up, not elapsed: candidate naming fires one call per
            candidate from separate goroutines, so the calls of one turn do
            not lie end to end. The trace's own clock is the wall time. */}
        {anyMs && <Kpi value={ms(totalMs)} label="added up over the calls" />}
        {u.cached_tokens != null && u.cached_tokens > 0 && <Kpi value={num(u.cached_tokens)} label="served from cache" />}
        {/* Only when there was any: both deployments answered with zero
            reasoning tokens on every call measured so far, and a row of
            zeroes on every turn would be noise around the figures that
            move. The count is recorded either way. */}
        {thought > 0 && <Kpi value={num(thought)} label="spent thinking" />}
      </div>

      <Section title="Calls">
        <Row label="model calls" value={c.model} />
        <Row
          label={
            <>
              embedding calls
              <Info term="an embedding call" open={said === "embedding"} onToggle={() => toggle("embedding")} />
            </>
          }
          value={c.embedding}
        />
        {said === "embedding" && <Said>{glossary.embedding}</Said>}
        {c.lanes != null && (
          <Row
            label={
              <>
                search lanes
                <Info term="a search lane" open={said === "lanes"} onToggle={() => toggle("lanes")} />
              </>
            }
            value={c.lanes}
          />
        )}
        {said === "lanes" && <Said>{glossary.lanes}</Said>}
        {c.hops != null && (
          <Row
            label={
              <>
                reference hops
                <Info term="a reference hop" open={said === "hops"} onToggle={() => toggle("hops")} />
              </>
            }
            value={c.hops}
          />
        )}
        {said === "hops" && <Said>{glossary.hops}</Said>}
      </Section>

      <div className="mt-5">
        <h3 className="mb-2 text-[11px] font-medium tracking-[.1em] text-faint uppercase">What each call cost</h3>
        <div className="overflow-x-auto">
          <table className="w-full border-collapse font-mono text-xs">
            <thead>
              <tr className="text-faint">
                <th className="border-b border-border-soft pb-1.5 text-left font-normal">call</th>
                <th className="border-b border-border-soft pb-1.5 pl-3 text-right font-normal whitespace-nowrap">in</th>
                {anyCached && <th className="border-b border-border-soft pb-1.5 pl-3 text-right font-normal whitespace-nowrap">cached</th>}
                <th className="border-b border-border-soft pb-1.5 pl-3 text-right font-normal whitespace-nowrap">out</th>
                {anyMs && <th className="border-b border-border-soft pb-1.5 pl-3 text-right font-normal whitespace-nowrap">took</th>}
                {u.cost_usd != null && <th className="border-b border-border-soft pb-1.5 pl-3 text-right font-normal whitespace-nowrap">cost</th>}
              </tr>
            </thead>
            <tbody>
              {u.calls.map((x, k) => (
                <CallRow key={k} call={x} cached={anyCached} took={anyMs} priced={u.cost_usd != null} />
              ))}
              <tr className="text-ink-dim">
                <td className="border-t border-border-soft pt-1.5">{u.calls.length} calls</td>
                <td className="border-t border-border-soft pt-1.5 pl-3 text-right tabular-nums whitespace-nowrap">{num(u.prompt_tokens)}</td>
                {anyCached && (
                  <td className="border-t border-border-soft pt-1.5 pl-3 text-right tabular-nums whitespace-nowrap">
                    {u.cached_tokens != null ? num(u.cached_tokens) : "–"}
                  </td>
                )}
                <td className="border-t border-border-soft pt-1.5 pl-3 text-right tabular-nums whitespace-nowrap">{num(u.completion_tokens)}</td>
                {anyMs && <td className="border-t border-border-soft pt-1.5 pl-3 text-right tabular-nums whitespace-nowrap">{ms(totalMs)}</td>}
                {u.cost_usd != null && (
                  <td className="border-t border-border-soft pt-1.5 pl-3 text-right tabular-nums whitespace-nowrap">{money(u.cost_usd)}</td>
                )}
              </tr>
            </tbody>
          </table>
        </div>
        {/* The cache note hangs under the table rather than off its header:
            a ⓘ inside a column header is the one place it has no room. */}
        {anyCached && (
          <p className="mt-2 font-sans text-[12.5px] text-faint">
            <button
              type="button"
              aria-label="What a cached prompt means"
              aria-expanded={said === "cached"}
              onClick={() => toggle("cached")}
              className="text-faint underline decoration-dotted underline-offset-2 hover:text-muted"
            >
              What does cached mean?
            </button>
            {thought > 0 && (
              <>
                {" · "}
                <button
                  type="button"
                  aria-label="What thinking means"
                  aria-expanded={said === "reasoning"}
                  onClick={() => toggle("reasoning")}
                  className="text-faint underline decoration-dotted underline-offset-2 hover:text-muted"
                >
                  What does thinking mean?
                </button>
              </>
            )}
          </p>
        )}
        {said === "cached" && <Said>{glossary.cached}</Said>}
        {said === "reasoning" && <Said>{glossary.reasoning}</Said>}
      </div>

      {system != null && sources != null && question != null && (
        <Section title="What filled the answer call">
          {answer && (
            <Row
              label={
                <>
                  context used
                  <Info term="the context" open={said === "context"} onToggle={() => toggle("context")} />
                </>
              }
              value={
                window > 0
                  ? `${num(answer.prompt_tokens)} of ${num(window)} · ${Math.max(
                      Math.round((answer.prompt_tokens / window) * 100),
                      1,
                    )}%`
                  : num(answer.prompt_tokens)
              }
            />
          )}
          {said === "context" && <Said>{glossary.context}</Said>}
          <Bar system={system} sources={sources} question={question} />
          <p className="col-span-full mt-2.5 font-sans text-[11.5px] leading-relaxed text-faint">
            The total is what the endpoint billed. The three parts are counted here at four characters per token, so they
            will not add up to it exactly.
          </p>
        </Section>
      )}

      {int(gather, "budget") != null && (
        <Section title="Budgets">
          <Row
            label={
              <>
                source budget
                <Info term="the source budget" open={said === "budget"} onToggle={() => toggle("budget")} />
              </>
            }
            value={`${num(int(gather, "tokens") ?? 0)} of ${num(int(gather, "budget")!)}`}
          />
          {said === "budget" && <Said>{glossary.budget}</Said>}
        </Section>
      )}
    </>
  );
}

function CallRow({ call, cached, took, priced }: { call: UsageCall; cached: boolean; took: boolean; priced: boolean }) {
  return (
    <tr className="text-muted">
      <td className="py-1 text-ink-dim">
        {call.step}
        <span className="ml-2 text-faint">{call.model}</span>
      </td>
      <td className="py-1 pl-3 text-right tabular-nums whitespace-nowrap">{num(call.prompt_tokens)}</td>
      {cached && (
        <td className="py-1 pl-3 text-right tabular-nums whitespace-nowrap">{call.cached_tokens != null ? num(call.cached_tokens) : "–"}</td>
      )}
      <td className="py-1 pl-3 text-right tabular-nums whitespace-nowrap">{call.completion_tokens > 0 ? num(call.completion_tokens) : "–"}</td>
      {took && <td className="py-1 pl-3 text-right tabular-nums whitespace-nowrap">{call.ms != null ? ms(call.ms) : "–"}</td>}
      {priced && <td className="py-1 pl-3 text-right tabular-nums whitespace-nowrap">{call.cost_usd != null ? money(call.cost_usd) : "–"}</td>}
    </tr>
  );
}

/** ThreadStats is the "Thread" side: the same calls, grouped by what they
 * were for rather than listed in the order they happened. */
function ThreadStats({ turns }: { turns: Turn[] }) {
  const withUsage = turns.filter((t) => t.usage);
  if (withUsage.length === 0) return <p className="mt-4 text-[13px] text-muted">This thread has no usage on record.</p>;

  const total = withUsage.reduce((n, t) => n + t.usage!.total_tokens, 0);
  const priced = withUsage.some((t) => t.usage!.cost_usd != null);
  const cost = withUsage.reduce((n, t) => n + (t.usage!.cost_usd ?? 0), 0);
  const cached = withUsage.reduce((n, t) => n + (t.usage!.cached_tokens ?? 0), 0);

  const byStep = new Map<string, number>();
  for (const t of withUsage) {
    for (const c of t.usage!.calls) {
      byStep.set(c.step, (byStep.get(c.step) ?? 0) + c.prompt_tokens + c.completion_tokens);
    }
  }
  const steps = [...byStep.entries()].sort((a, b) => b[1] - a[1]);
  const biggest = steps.length > 0 ? steps[0][1] : 1;

  return (
    <>
      <div className="grid grid-cols-2 gap-2">
        <Kpi value={num(total)} label={`tokens over ${withUsage.length} turn${withUsage.length === 1 ? "" : "s"}`} />
        {priced && <Kpi value={money(cost)} label="at list price" />}
        <Kpi value={num(Math.round(total / withUsage.length))} label="per turn, average" />
        {cached > 0 && <Kpi value={num(cached)} label="served from cache" />}
      </div>

      <Section title="Where the tokens went">
        {steps.map(([step, n]) => (
          <div key={step} className="col-span-full">
            <div className="grid grid-cols-[1fr_auto] items-center gap-2.5">
              <div className="py-1 text-ink-dim">{step}</div>
              <div className="py-1 text-right text-muted tabular-nums">
                {num(n)} · {Math.max(Math.round((n / total) * 100), 1)}%
              </div>
            </div>
            <div className="h-1 overflow-hidden rounded-full bg-active">
              <span className="block h-full bg-accent-strong" style={{ width: `${(n / biggest) * 100}%` }} />
            </div>
          </div>
        ))}
      </Section>

      <Section title="Per turn">
        {withUsage.map((t, i) => (
          <Row
            key={i}
            label={`Turn ${turns.indexOf(t) + 1}`}
            value={
              <>
                {num(t.usage!.total_tokens)}
                {t.usage!.cost_usd != null && <span className="ml-2">{money(t.usage!.cost_usd)}</span>}
              </>
            }
          />
        ))}
      </Section>
    </>
  );
}

export type StatsTarget = { kind: "turn"; index: number } | { kind: "thread" };

/** StatsPane is the drawer. Escape closes it, the way the modals do. */
export function StatsPane({ target, turns, onClose }: { target: StatsTarget; turns: Turn[]; onClose: () => void }) {
  const [tab, setTab] = useState<"turn" | "thread">(target.kind === "turn" ? "turn" : "thread");
  const [said, setSaid] = useState<string | null>(null);
  const toggle = (t: string) => setSaid(said === t ? null : t);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const turn = target.kind === "turn" ? turns[target.index] : null;

  return (
    <>
      <div className="fixed inset-0 z-20 bg-black/45" onClick={onClose} aria-hidden="true" />
      <aside
        role="dialog"
        aria-label="Token stats"
        className="thin-scroll fixed top-0 right-0 bottom-0 z-30 w-[440px] max-w-[94vw] overflow-auto border-l border-border bg-panel px-5 pt-4 pb-8 shadow-panel"
      >
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 className="font-serif text-[19px] font-medium text-ink">
              {tab === "turn" && turn ? `Turn ${target.kind === "turn" ? target.index + 1 : 1}` : "Thread"}
            </h2>
            <p className="mt-0.5 text-[12.5px] text-faint">
              {tab === "turn" && turn
                ? turn.question
                : `${turns.filter((t) => t.usage).length} turn${turns.filter((t) => t.usage).length === 1 ? "" : "s"} with usage on record`}
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close token stats"
            className="rounded-full border border-border bg-panel px-3 py-1 text-[13px] text-ink-dim hover:border-elevated-border hover:bg-active"
          >
            Close
          </button>
        </div>

        {turn && (
          <div className="mt-3.5 inline-flex gap-0.5 rounded-full border border-border bg-bg p-0.5">
            {(["turn", "thread"] as const).map((t) => (
              <button
                key={t}
                type="button"
                aria-pressed={tab === t}
                onClick={() => setTab(t)}
                className={
                  "rounded-full px-3.5 py-1 text-[12.5px] " +
                  (tab === t ? "bg-active text-ink-dim" : "text-faint hover:text-muted")
                }
              >
                {t === "turn" ? "This turn" : "Thread"}
              </button>
            ))}
          </div>
        )}

        <div className="mt-1">
          {tab === "turn" && turn ? <TurnStats turn={turn} said={said} toggle={toggle} /> : <ThreadStats turns={turns} />}
        </div>

        <p className="mt-5 font-sans text-[11.5px] leading-relaxed text-faint">
          Prices are the registry's list price, USD per million tokens: the deployments at MiMo's own API whatever
          endpoint they were called at, embeddings at theirs. Not a bill: the provider's invoice is.
        </p>
      </aside>
    </>
  );
}
