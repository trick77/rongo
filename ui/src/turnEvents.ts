import { withStep, type Turn, type Usage } from "./turns";

/**
 * What one event of a turn's stream does to the turn it is written into.
 *
 * Pure: the turn in, the turn out, the clock passed in. Everything an event
 * does BESIDES changing the turn — parking the stream under its thread,
 * telling the rail a title arrived, noting that the turn ended — stays with
 * the stream in Ask, which is the only thing that knows where the reader is
 * standing. An event that changes nothing about the turn hands it back as it
 * was.
 *
 * The payload is whatever the server sent, already parsed. It is read
 * defensively, field by field, because a turn must survive an event shaped
 * by a newer or an older backend than this page.
 */
export function applyEvent(t: Turn, name: string, payload: any, now: number): Turn {
  switch (name) {
    case "thread":
      // The turn is on record now, in the language the record took. That is
      // not always the one that was asked for — a thread answers in the
      // language of its first turn — so the turn on screen, and with it the
      // composer, follow the server rather than the guess.
      return { ...t, recorded: true, language: payload.language ?? t.language };
    case "status":
      return withStep(t, payload.step, now, typeof payload.at === "number" ? payload.at : undefined);
    case "detail": {
      // What the step found, attached to the LATEST step of that name: a
      // step's detail arrives once the step is done, and "searching" can run
      // twice in a comparison turn.
      const i = t.steps.map((s) => s.step).lastIndexOf(payload.step);
      if (i < 0) return t;
      const steps = t.steps.slice();
      steps[i] = { ...steps[i], detail: payload.detail ?? undefined };
      return { ...t, steps };
    }
    case "notice":
      return { ...t, notice: payload.text ?? "" };
    case "token":
      return { ...t, text: t.text + payload.text };
    case "citations":
      return { ...t, citations: payload ?? [] };
    case "followups":
      return { ...t, followups: payload ?? [] };
    case "memory":
      return {
        ...t,
        memory: {
          id: payload.id ?? null,
          text: payload.text ?? "",
          scope: payload.scope ?? "",
          replaced: payload.replaced ?? [],
          removed: payload.removed ?? [],
          scopeDropped: payload.scope_dropped ?? "",
        },
      };
    case "usage":
      return { ...t, usage: payload as Usage };
    case "clarification":
      return {
        ...t,
        messageId: payload.message_id,
        clarification: {
          messageId: payload.message_id,
          candidates: payload.candidates ?? [],
          tooBroad: payload.too_broad ?? false,
        },
      };
    case "error":
      // The id comes with the failure too, not only with done: asking again
      // is another attempt at THIS question, and the request can only say so
      // if the turn knows which row it is.
      return { ...t, error: payload.message, done: true, endedAt: now, messageId: payload.message_id ?? t.messageId };
    case "done":
      return {
        ...t,
        done: true,
        endedAt: t.endedAt ?? now,
        messageId: payload.message_id ?? t.messageId,
        // A re-explain opens no thread event, and it too is filed in the
        // thread's language rather than the one it asked for.
        recorded: true,
        language: payload.language ?? t.language,
        sourceless: payload.sourceless === true,
      };
    default:
      // "title" among them: the rail and the header read the list, and the
      // turn itself has nothing to keep of it.
      return t;
  }
}

/** The two events a turn is meant to end on. A stream that closes without one
 * was cut off. */
export function endsTurn(name: string): boolean {
  return name === "error" || name === "done";
}
