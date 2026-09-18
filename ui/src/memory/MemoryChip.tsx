import { useState } from "react";

import type { TurnMemory } from "../turns";
import { forgetMemory } from "./api";

/**
 * What a turn did to the reader's memory, under the answer: the rule kept,
 * what it replaced, what was forgotten, and the one way back. Undo deletes
 * the rule this turn saved and nothing else — a replaced rule does not come
 * back; the reader re-states it in chat if that was the wrong call.
 *
 * Not ochre: nothing here waits on the reader. The chip is part of the
 * record (the turn keeps the rule's id), so it is there after a reload and
 * gone once the rule is.
 */
export default function MemoryChip({
  memory,
  readOnly = false,
}: {
  memory: TurnMemory;
  /** No undo: a page with no actions, or a turn that is not this reader's. */
  readOnly?: boolean;
}) {
  const [forgotten, setForgotten] = useState(false);
  const [busy, setBusy] = useState(false);

  async function undo() {
    if (memory.id === null) return;
    setBusy(true);
    try {
      if (await forgetMemory(memory.id)) setForgotten(true);
    } catch {
      // The rule stays; the chip says so by staying as it was.
    } finally {
      setBusy(false);
    }
  }

  if (forgotten) {
    return (
      <div role="note" aria-label="Memory" className="mt-4 inline-flex items-baseline gap-2 rounded-ui-sm border border-border bg-panel px-3 py-1.5 text-[13px] text-muted">
        <span className="font-medium text-ink-dim">Forgotten</span>
        <span className="line-through">{memory.text}</span>
      </div>
    );
  }

  const kept = memory.text !== "";
  return (
    <div role="note" aria-label="Memory" className="mt-4 flex max-w-[68ch] flex-wrap items-baseline gap-x-2 gap-y-1 rounded-ui-sm border border-border bg-panel px-3 py-1.5 text-[13px] text-muted">
      {kept && (
        <>
          <span className="font-medium text-ink-dim">Remembered</span>
          <span>{memory.text}</span>
          {memory.scope && <span className="rounded-full bg-active px-2 text-[11.5px]">{memory.scope}</span>}
          {memory.scopeDropped && (
            <span className="text-faint">
              {memory.scopeDropped} is not indexed, so it applies everywhere
            </span>
          )}
        </>
      )}
      {memory.replaced.length > 0 && (
        <span className="text-faint">
          {kept ? "replaces" : "Replaced"} {memory.replaced.map((r) => `"${r}"`).join(", ")}
        </span>
      )}
      {memory.removed.length > 0 && (
        <span className={kept ? "text-faint" : "font-medium text-ink-dim"}>
          {kept ? "forgot" : "Forgotten"}{" "}
          <span className="font-normal text-faint">{memory.removed.map((r) => `"${r}"`).join(", ")}</span>
        </span>
      )}
      {kept && memory.id !== null && !readOnly && (
        <button
          type="button"
          disabled={busy}
          onClick={() => void undo()}
          className="text-accent-strong underline-offset-[3px] hover:underline disabled:opacity-50"
        >
          Undo
        </button>
      )}
    </div>
  );
}
