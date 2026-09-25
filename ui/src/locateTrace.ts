/**
 * The locate loop in the reader's words: what it looked up, what each lookup
 * came back with, and what its conclusion did to the answer's sources. Every
 * sentence is built from facts the backend reported; the loop model's own
 * conclusion is never shown - it is prose in the question's language making
 * claims about code nothing cites.
 */

/** A file as the trace names it: the base name, the rest on hover. */
export type LocateFile = { name: string; title: string };

export type LocateLine = {
  /** What was asked, up to a file name when there is one. */
  lead: string;
  file?: LocateFile;
  /** What follows the file name. */
  tail?: string;
  /** What came of it. */
  result: string;
};

export type LocateSummary = {
  header: string;
  steps: LocateLine[];
  /** Why the loop stopped before it was done, as a sentence. */
  stopped?: string;
  /** What the conclusion did, as a sentence around an optional file. */
  outcome?: { lead: string; file?: LocateFile; tail: string };
};

type WireStep = {
  tool?: string;
  arg?: string;
  repo?: string;
  path?: string;
  line?: number;
  matches?: number;
  held?: number;
  added?: number;
  not_run?: string;
  cut?: boolean;
  /** Stored before counts existed: the call landed something, how much is not known. */
  landed?: boolean;
};

type WirePlace = { repo?: string; path?: string; line?: number };

const str = (v: unknown): string => (typeof v === "string" ? v : "");
const num = (v: unknown): number => (typeof v === "number" ? v : 0);
const strs = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : []);
const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

function fileOf(path: string, repo?: string): LocateFile {
  const name = path.split("/").pop() || path;
  return { name, title: repo ? `${repo} · ${path}` : path };
}

// A refused call that named nothing is one more of its kind.
const another: Record<string, string> = {
  grep: "another code search",
  search: "another search by meaning",
  symbol: "another symbol lookup",
  read: "another file",
};

function describe(s: WireStep): LocateLine {
  if (s.not_run && !s.arg && !s.path) {
    const where = s.repo ? ` in ${s.repo}` : "";
    return { lead: `${another[s.tool ?? ""] ?? `another call to ${s.tool || "a tool"}`}${where}`, result: resultOf(s) };
  }
  const where = s.repo && s.tool !== "read" ? ` in ${s.repo}` : "";
  let line: Omit<LocateLine, "result">;
  switch (s.tool) {
    case "grep":
      line = { lead: s.arg ? `searched the code for "${s.arg}"${where}` : `searched the code${where}` };
      break;
    case "search":
      line = { lead: s.arg ? `searched by meaning for "${s.arg}"${where}` : `searched by meaning${where}` };
      break;
    case "symbol":
      line = { lead: s.arg ? `looked up the symbol ${s.arg}${where}` : `looked up a symbol${where}` };
      break;
    case "read":
      line = s.path
        ? { lead: "opened ", file: fileOf(s.path, s.repo), tail: s.line ? ` at line ${s.line}` : "" }
        : { lead: "opened a file" };
      break;
    default:
      line = { lead: `called ${s.tool || "a tool"}` };
  }
  return { ...line, result: resultOf(s) };
}

function resultOf(s: WireStep): string {
  if (s.not_run) return `not run: ${s.not_run}`;
  const added = num(s.added);
  if (s.cut) return `the token budget ran out partway, ${plural(added, "source", "sources")} added`;
  if (added > 0) return `added ${plural(added, "source", "sources")}`;
  if (s.landed) return "added sources";
  if (num(s.matches) > 0) return plural(num(s.matches), "matching line", "matching lines");
  if (num(s.held) > 0) return "already among the sources";
  return "nothing";
}

// Stored before the steps existed, a call was a label: "grep(x) 64 lines",
// "read(path:40)", "search(q)", "symbol(n)", and a refusal carried its reason
// inside the parentheses. Parsed into the same steps, so an old thread reads
// like a new one.
const labelShape = /^(\w+)\((.*)\)(?: (\d+) lines)?$/;

const refusalReasons: [RegExp, string][] = [
  [/^over the call limit$/, "over the limit of calls in one round"],
  [/^not tried$/, "the token budget for this step was spent"],
  [/^unparseable$/, "the call was malformed"],
  [/^unknown tool$/, "there is no tool by that name"],
  [/: outside this turn's repositories$/, "that repository is outside this turn"],
  [/: not in the index$/, "that repository is not in the index"],
  [/^$/, "it named nothing to look up"],
];

function fromLabel(label: string, refused: boolean): WireStep {
  const m = labelShape.exec(label);
  if (!m) return { tool: label, not_run: refused ? "not run" : undefined };
  const [, tool, inner, lines] = m;
  if (refused) {
    const reason = refusalReasons.find(([re]) => re.test(inner));
    if (reason) {
      const repo = inner.includes(": ") ? inner.slice(0, inner.indexOf(": ")) : undefined;
      return { tool, repo, not_run: reason[1] };
    }
    return { tool, not_run: "not run" };
  }
  if (tool === "read") {
    const at = inner.lastIndexOf(":");
    return at > 0 ? { tool, path: inner.slice(0, at), line: Number(inner.slice(at + 1)) || 0 } : { tool, path: inner };
  }
  return { tool, arg: inner, matches: lines ? Number(lines) : 0 };
}

function legacySteps(detail: Record<string, unknown>): WireStep[] {
  const landed = new Set(strs(detail.locate_landed));
  const empty = new Set(strs(detail.locate_empty));
  // A call that ran, landed nothing new and was not empty found only what
  // the turn already held.
  const ran = strs(detail.locate_calls).map((label): WireStep => {
    const step = fromLabel(label, false);
    if (landed.has(label)) return { ...step, landed: true };
    if (!empty.has(label) && !step.matches) return { ...step, held: 1 };
    return step;
  });
  return [...ran, ...strs(detail.locate_refused).map((label) => fromLabel(label, true))];
}

// The old record's pointer was the model's sentence. Only its shape is read:
// NOT FOUND, or FOUND and the first path:LINE in it. The prose is dropped.
function legacyOutcome(found: string): LocateSummary["outcome"] {
  const text = found.trim();
  if (text.startsWith("NOT FOUND")) return notFound;
  if (!text.startsWith("FOUND:")) return undefined;
  const tokens = text.slice("FOUND:".length).trim().split(/\s+/);
  const at = tokens.findIndex((t) => /^[\w.\-/]+\.\w+:\d+/.test(t));
  if (at < 0) return undefined;
  const [, path, line] = /^([\w.\-/]+\.\w+):(\d+)/.exec(tokens[at])!;
  const repo = at > 0 && !tokens[at - 1].includes("/") ? tokens[at - 1] : undefined;
  return { lead: "Named ", file: fileOf(path, repo), tail: ` line ${line}${repo ? ` (${repo})` : ""} as the place to write the answer from.` };
}

const notFound = {
  lead: "Did not find the exact place. The answer uses the search results in their usual order.",
  tail: "",
};

function outcomeOf(detail: Record<string, unknown>, ran: number): LocateSummary["outcome"] {
  const outcome = str(detail.locate_outcome);
  const place = (detail.locate_place ?? {}) as WirePlace;
  if (outcome === "pointed" && place.path) {
    return {
      lead: "Found it: ",
      file: fileOf(place.path, place.repo),
      tail: ` line ${num(place.line)}${place.repo ? ` (${place.repo})` : ""}. The answer is written starting from that source.`,
    };
  }
  if (outcome === "not_found") return notFound;
  if (outcome === "unpinned") {
    return { lead: "Named a place that is not among the sources, so their order was left unchanged.", tail: "" };
  }
  if (typeof detail.locate_found === "string") return legacyOutcome(detail.locate_found);
  if (ran > 0 && Array.isArray(detail.locate_steps)) {
    return { lead: "Came to no conclusion, so the sources keep their usual order.", tail: "" };
  }
  return undefined;
}

/** The locate block of a gathering step, or null on a turn the loop did not run. */
export function locateSummary(detail: Record<string, unknown>): LocateSummary | null {
  const rounds = num(detail.locate_rounds);
  const stop = str(detail.locate);
  if (rounds === 0 && stop === "") return null;
  const wire = Array.isArray(detail.locate_steps) ? (detail.locate_steps as WireStep[]) : legacySteps(detail);
  const ran = wire.filter((s) => !s.not_run).length;
  const header =
    ran === 0
      ? "Looked for the exact place in the code; no lookup ran"
      : `Looked for the exact place in the code, ${plural(ran, "lookup", "lookups")} in ${plural(rounds, "round", "rounds")}`;
  return {
    header,
    steps: wire.map(describe),
    stopped: stop === "" ? undefined : stop === "call failed" ? "Stopped early: the model call failed." : `Stopped early: ${stop}.`,
    outcome: outcomeOf(detail, ran),
  };
}
