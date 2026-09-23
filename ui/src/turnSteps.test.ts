import { describe, it, expect } from "vitest";
import { withStep, type Turn } from "./turns";

const turn = (): Turn => ({ steps: [] }) as unknown as Turn;

describe("withStep", () => {
  it("keeps the server's gap between steps whatever the arrival times", () => {
    // Buffered on the way: understanding and searching arrive 100ms apart,
    // though the server announced them 70s apart.
    let t = withStep(turn(), "understanding", 5_000, 1_000);
    t = withStep(t, "searching", 5_100, 71_000);
    expect(t.steps[1].at - t.steps[0].at).toBe(70_000);
  });

  it("puts the steps on the browser's clock from the least delayed arrival", () => {
    // The first event came 4s late, the second on time (arrival equals its
    // server time plus a clock offset of 100ms): the offset is the smaller one.
    let t = withStep(turn(), "understanding", 5_000, 1_000);
    t = withStep(t, "searching", 71_100, 71_000);
    expect(t.steps[0].at).toBe(1_100);
    expect(t.steps[1].at).toBe(71_100);
  });

  it("falls back to the arrival time when the server sent none", () => {
    const t = withStep(turn(), "understanding", 5_000);
    expect(t.steps[0].at).toBe(5_000);
  });
});
