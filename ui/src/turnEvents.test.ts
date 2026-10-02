import { describe, it, expect } from "vitest";
import { applyEvent, endsTurn } from "./turnEvents";
import { freshTurn, type Turn } from "./turns";

const turn = (over: Partial<Turn> = {}): Turn => ({ ...freshTurn("How?", "ba", "en"), startedAt: 1000, ...over });

describe("applyEvent", () => {
  it("appends each token to the answer", () => {
    const t = applyEvent(applyEvent(turn(), "token", { text: "Through " }, 1), "token", { text: "a grant." }, 2);

    expect(t.text).toBe("Through a grant.");
  });

  it("files the turn in the language the record took, not the one asked for", () => {
    const t = applyEvent(turn({ language: "en" }), "thread", { thread_id: "7", language: "de" }, 1);

    expect(t.recorded).toBe(true);
    expect(t.language).toBe("de");
    // A thread event from a backend that sends no language leaves the guess.
    expect(applyEvent(turn({ language: "fr" }), "thread", { thread_id: "7" }, 1).language).toBe("fr");
  });

  it("puts a step on the timeline and its detail on the latest step of that name", () => {
    // Given a comparison turn, which searches twice
    let t = applyEvent(turn(), "status", { step: "searching" }, 1100);
    t = applyEvent(t, "detail", { step: "searching", detail: { hits: 3 } }, 1150);
    t = applyEvent(t, "status", { step: "searching" }, 1200);

    // When the second search reports what it found
    t = applyEvent(t, "detail", { step: "searching", detail: { hits: 9 } }, 1250);

    // Then each search keeps its own
    expect(t.steps.map((s) => s.detail)).toEqual([{ hits: 3 }, { hits: 9 }]);
    expect(t.steps.map((s) => s.at)).toEqual([1100, 1200]);
  });

  it("leaves the turn alone for a detail of a step it never saw", () => {
    const before = applyEvent(turn(), "status", { step: "searching" }, 1100);

    expect(applyEvent(before, "detail", { step: "routing", detail: { x: 1 } }, 1200)).toBe(before);
  });

  it("takes the server's clock for a step when it sends one", () => {
    // The step left the server at 5000 and arrived at 9000; the next left at
    // 5400 and arrived, less delayed, at 9300.
    let t = applyEvent(turn(), "status", { step: "understanding", at: 5000 }, 9000);
    t = applyEvent(t, "status", { step: "searching", at: 5400 }, 9300);

    // The steps are 400 ms apart, as the server ran them, whatever the
    // buffering did to their arrival.
    expect(t.steps[1].at - t.steps[0].at).toBe(400);
  });

  it("ends the turn on done, with what the record says about it", () => {
    const t = applyEvent(turn({ text: "x" }), "done", { message_id: 5, language: "de", sourceless: true }, 4000);

    expect(t).toMatchObject({ done: true, endedAt: 4000, messageId: 5, recorded: true, language: "de", sourceless: true });
    // An ending already stamped is kept, and a done without the flag is a
    // turn with sources.
    const kept = applyEvent(turn({ endedAt: 3000, messageId: 9 }), "done", {}, 4000);
    expect(kept).toMatchObject({ endedAt: 3000, messageId: 9, sourceless: false });
  });

  it("ends the turn on error and keeps the row it failed on, so a retry joins it", () => {
    const t = applyEvent(turn(), "error", { message: "The turn failed.", message_id: 12 }, 2000);

    expect(t).toMatchObject({ error: "The turn failed.", done: true, endedAt: 2000, messageId: 12 });
    expect(applyEvent(turn({ messageId: 3 }), "error", { message: "x" }, 1).messageId).toBe(3);
  });

  it("opens a card, too broad or not", () => {
    const t = applyEvent(turn(), "clarification", { message_id: 8, too_broad: true, candidates: [{ idx: 0 }] }, 1);

    expect(t.messageId).toBe(8);
    expect(t.clarification).toEqual({ messageId: 8, candidates: [{ idx: 0 }], tooBroad: true });
    expect(applyEvent(turn(), "clarification", { message_id: 8 }, 1).clarification).toEqual({
      messageId: 8,
      candidates: [],
      tooBroad: false,
    });
  });

  it("records what the turn did to the reader's memory", () => {
    const t = applyEvent(turn(), "memory", { id: 4, text: "Answer briefly.", scope_dropped: "nope" }, 1);

    expect(t.memory).toEqual({ id: 4, text: "Answer briefly.", scope: "", replaced: [], removed: [], scopeDropped: "nope" });
  });

  it("takes notice, citations, follow-ups and usage as sent", () => {
    let t = applyEvent(turn(), "notice", { text: "loom is not indexed." }, 1);
    t = applyEvent(t, "citations", [{ marker: 1 }], 1);
    t = applyEvent(t, "followups", ["And then?"], 1);
    t = applyEvent(t, "usage", { total_tokens: 42 }, 1);

    expect(t.notice).toBe("loom is not indexed.");
    expect(t.citations).toEqual([{ marker: 1 }]);
    expect(t.followups).toEqual(["And then?"]);
    expect(t.usage).toEqual({ total_tokens: 42 });
    // An event with no body empties rather than breaks.
    expect(applyEvent(t, "citations", null, 1).citations).toEqual([]);
    expect(applyEvent(t, "followups", null, 1).followups).toEqual([]);
    expect(applyEvent(t, "notice", {}, 1).notice).toBe("");
  });

  it("hands back the very turn for an event that says nothing about it", () => {
    const t = turn();

    expect(applyEvent(t, "title", { title: "Sign-in" }, 1)).toBe(t);
    expect(applyEvent(t, "something-newer", {}, 1)).toBe(t);
  });
});

describe("endsTurn", () => {
  it("is true for the two ways a turn is meant to end", () => {
    expect(["done", "error", "token", "usage"].map(endsTurn)).toEqual([true, true, false, false]);
  });
});
