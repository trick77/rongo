import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import {
  RAIL_DEFAULT,
  RAIL_MAX,
  RAIL_MIN,
  RailResizer,
  clampRail,
  displayedWidth,
  storedRailWidth,
} from "./RailResizer";

const handle = () => document.querySelector(".rail-resizer") as HTMLElement;
const pref = () =>
  document.documentElement.style.getPropertyValue("--rail-pref");
const stored = () => localStorage.getItem("rongo.rail-width");
const valuenow = () => Number(handle().getAttribute("aria-valuenow"));

/** Press on the handle at the current border, so a move to x yields a width of x. */
const grab = (h: HTMLElement, pointerId = 1, pointerType = "mouse") =>
  fireEvent.pointerDown(h, {
    pointerId,
    pointerType,
    button: 0,
    clientX: valuenow(),
  });

/** jsdom reports 1024 by default; the clamp keys on 40vw, so tests set it. */
function withViewport(width: number) {
  Object.defineProperty(document.documentElement, "clientWidth", {
    configurable: true,
    value: width,
  });
}

beforeEach(() => {
  localStorage.clear();
  withViewport(1600); // 40vw = 640, so the 520 max is the binding cap
  document.documentElement.style.removeProperty("--rail-pref");
});
afterEach(() => {
  cleanup();
  document.body.classList.remove("resizing");
  vi.restoreAllMocks();
});

describe("clampRail", () => {
  it("holds the bounds and rounds", () => {
    expect(clampRail(400)).toBe(400);
    expect(clampRail(20)).toBe(RAIL_MIN);
    expect(clampRail(9999)).toBe(RAIL_MAX);
    expect(clampRail(362.6)).toBe(363);
  });

  // The viewport cap is the CSS clamp on --rail-w, deliberately not here: clamping
  // to the window and storing the result would lose the preference on a rotation.
  it("ignores the viewport", () => {
    expect(clampRail(RAIL_MAX)).toBe(RAIL_MAX);
  });
});

describe("storedRailWidth", () => {
  it("falls back to the default when nothing is stored", () => {
    expect(storedRailWidth()).toBe(RAIL_DEFAULT);
  });

  it("clamps and rounds what it reads", () => {
    localStorage.setItem("rongo.rail-width", "9999");
    expect(storedRailWidth()).toBe(RAIL_MAX);
  });

  // A hand-edited or half-written value must not widen the rail to NaN.
  it("rejects junk", () => {
    localStorage.setItem("rongo.rail-width", "wide please");
    expect(storedRailWidth()).toBe(RAIL_DEFAULT);
  });

  it("survives a storage that throws, as Safari's private mode does", () => {
    vi.spyOn(localStorage, "getItem").mockImplementation(() => {
      throw new Error("private mode");
    });
    expect(storedRailWidth()).toBe(RAIL_DEFAULT);
  });
});

describe("RailResizer", () => {
  it("exposes the separator semantics screen readers need", () => {
    render(<RailResizer />);
    const h = handle();
    expect(h.getAttribute("role")).toBe("separator");
    expect(h.getAttribute("aria-orientation")).toBe("vertical");
    expect(h.getAttribute("aria-valuenow")).toBe(String(RAIL_DEFAULT));
    expect(h.getAttribute("aria-valuemin")).toBe(String(RAIL_MIN));
    expect(h.getAttribute("aria-valuemax")).toBe(String(RAIL_MAX));
    expect(h.tabIndex).toBe(0);
  });

  // The rail is the layout only from lg; below it the drawer is a fixed 300px
  // off-canvas box, where a resizer would drag an edge nobody can see.
  it("is hidden below the lg breakpoint", () => {
    render(<RailResizer />);
    expect(handle().className).toContain("hidden");
    expect(handle().className).toContain("lg:block");
  });

  it("paints the stored width on mount", () => {
    localStorage.setItem("rongo.rail-width", "420");
    render(<RailResizer />);
    expect(pref()).toBe("420px");
    expect(valuenow()).toBe(420);
  });

  it("widens and narrows with the arrow keys, and persists", () => {
    render(<RailResizer />);
    fireEvent.keyDown(handle(), { key: "ArrowRight" });
    expect(pref()).toBe(RAIL_DEFAULT + 16 + "px");
    expect(stored()).toBe(String(RAIL_DEFAULT + 16));

    fireEvent.keyDown(handle(), { key: "ArrowLeft" });
    expect(pref()).toBe(RAIL_DEFAULT + "px");
    expect(stored()).toBe(String(RAIL_DEFAULT));
  });

  it("clamps at both ends instead of running away", () => {
    localStorage.setItem("rongo.rail-width", String(RAIL_MAX));
    render(<RailResizer />);
    fireEvent.keyDown(handle(), { key: "ArrowRight" });
    expect(valuenow()).toBe(RAIL_MAX);

    cleanup();
    localStorage.setItem("rongo.rail-width", String(RAIL_MIN));
    render(<RailResizer />);
    fireEvent.keyDown(handle(), { key: "ArrowLeft" });
    expect(valuenow()).toBe(RAIL_MIN);
  });

  it("ignores keys it does not own", () => {
    render(<RailResizer />);
    document.documentElement.style.removeProperty("--rail-pref");
    fireEvent.keyDown(handle(), { key: "a" });
    expect(pref()).toBe("");
  });

  it("resets to the default on Home", () => {
    localStorage.setItem("rongo.rail-width", "480");
    render(<RailResizer />);
    fireEvent.keyDown(handle(), { key: "Home" });
    expect(valuenow()).toBe(RAIL_DEFAULT);
    expect(stored()).toBe(String(RAIL_DEFAULT));
  });

  it("resizes on a pointer drag and stores once, on release", () => {
    render(<RailResizer />);
    const h = handle();
    grab(h);
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 420 });
    expect(pref()).toBe("420px");
    expect(stored()).toBeNull(); // nothing written mid-drag

    fireEvent.pointerUp(h, { pointerId: 1 });
    expect(valuenow()).toBe(420);
    expect(stored()).toBe("420");
    expect(document.body.classList.contains("resizing")).toBe(false);
  });

  it("clamps a drag past the edges", () => {
    render(<RailResizer />);
    const h = handle();
    grab(h);
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 20 });
    expect(pref()).toBe(RAIL_MIN + "px");
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 5000 });
    expect(pref()).toBe(RAIL_MAX + "px");
    fireEvent.pointerUp(h, { pointerId: 1 });
    expect(stored()).toBe(String(RAIL_MAX));
  });

  // Regression from ../transmission-ui: the double-tap window compared against a 0
  // sentinel, and performance.now() is milliseconds since load — so the very first
  // drag on a freshly loaded page was swallowed as a double tap.
  it("drags on the first interaction after load", () => {
    vi.spyOn(performance, "now").mockReturnValue(120);
    render(<RailResizer />);
    const h = handle();
    grab(h, 1, "touch");
    expect(document.body.classList.contains("resizing")).toBe(true);
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 420 });
    fireEvent.pointerUp(h, { pointerId: 1 });
    expect(valuenow()).toBe(420);
  });

  // Regression: pointerup used to commit the width captured when the drag began, so
  // a real browser (which batches the moves into one render) snapped back.
  it("commits the last dragged width, not the width at pointerdown", () => {
    render(<RailResizer />);
    const h = handle();
    grab(h);
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 420 });
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 470 });
    fireEvent.pointerUp(h, { pointerId: 1 });
    expect(valuenow()).toBe(470);
    expect(stored()).toBe("470");
    expect(pref()).toBe("470px");
  });

  it("ignores a move that is not part of a drag", () => {
    render(<RailResizer />);
    document.documentElement.style.removeProperty("--rail-pref");
    fireEvent.pointerMove(handle(), { pointerId: 1, clientX: 420 });
    expect(pref()).toBe("");
  });

  it("treats pointercancel like a release", () => {
    render(<RailResizer />);
    const h = handle();
    grab(h);
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 400 });
    fireEvent.pointerCancel(h, { pointerId: 1 });
    expect(stored()).toBe("400");
    expect(document.body.classList.contains("resizing")).toBe(false);
  });

  it("ignores a non-primary mouse button", () => {
    render(<RailResizer />);
    const h = handle();
    document.documentElement.style.removeProperty("--rail-pref");
    fireEvent.pointerDown(h, { pointerId: 1, pointerType: "mouse", button: 2 });
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 420 });
    expect(pref()).toBe("");
  });

  // Review finding in ../transmission-ui: the drag used the raw clientX, so grabbing
  // the handle off-centre snapped the border to the pointer.
  it("moves by the grab offset, not to the pointer", () => {
    render(<RailResizer />);
    const h = handle();
    fireEvent.pointerDown(h, {
      pointerId: 1,
      pointerType: "mouse",
      button: 0,
      clientX: RAIL_DEFAULT + 6, // 6px right of the border
    });
    fireEvent.pointerMove(h, { pointerId: 1, clientX: RAIL_DEFAULT + 36 });
    expect(pref()).toBe(RAIL_DEFAULT + 30 + "px"); // travelled +30, not to the pointer
    fireEvent.pointerUp(h, { pointerId: 1 });
    expect(valuenow()).toBe(RAIL_DEFAULT + 30);
  });

  // Review finding: a touch device emits a pixel of jitter during a plain tap.
  // Treating that as a drag nudged the edge and disarmed the double-tap reset.
  it("ignores tap jitter, so a jittery double tap still resets", () => {
    const clock = vi.spyOn(performance, "now");
    localStorage.setItem("rongo.rail-width", "480");
    render(<RailResizer />);
    const h = handle();

    clock.mockReturnValue(1000);
    fireEvent.pointerDown(h, {
      pointerId: 1,
      pointerType: "touch",
      clientX: 480,
    });
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 481 }); // 1px of jitter
    fireEvent.pointerUp(h, { pointerId: 1 });
    expect(valuenow()).toBe(480); // the tap moved nothing

    clock.mockReturnValue(1150);
    fireEvent.pointerDown(h, {
      pointerId: 1,
      pointerType: "touch",
      clientX: 480,
    });
    expect(valuenow()).toBe(RAIL_DEFAULT); // the reset still fires
  });

  // Review finding: releasePointerCapture throws NotFoundError once the pointer is
  // gone, which is the pointercancel case, and the throw skipped the commit.
  it("commits before it releases capture, and swallows a throw", () => {
    render(<RailResizer />);
    const h = handle();
    const order: string[] = [];
    h.hasPointerCapture = () => true;
    h.releasePointerCapture = () => {
      order.push("release");
      throw new DOMException("gone", "NotFoundError");
    };

    grab(h);
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 420 });
    fireEvent.pointerCancel(h, { pointerId: 1 });

    expect(valuenow()).toBe(420);
    expect(stored()).toBe("420"); // committed despite the throw
    expect(order).toEqual(["release"]);
  });

  // Review finding: a completed drag used to arm the double-tap window, so
  // re-grabbing within the window to fine-tune snapped back to the default.
  it("does not treat a re-grab right after a drag as a double tap", () => {
    const clock = vi.spyOn(performance, "now");
    render(<RailResizer />);
    const h = handle();
    clock.mockReturnValue(1000);
    grab(h);
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 420 });
    fireEvent.pointerUp(h, { pointerId: 1 });
    expect(valuenow()).toBe(420);

    clock.mockReturnValue(1100); // inside the tap window
    grab(h);
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 436 });
    fireEvent.pointerUp(h, { pointerId: 1 });
    expect(valuenow()).toBe(436); // fine-tuned, not reset
  });

  // Review finding: a second finger landing mid-drag hijacked the drag state and
  // left the painted width out of step with what was stored.
  it("ignores a second pointer during a drag", () => {
    render(<RailResizer />);
    const h = handle();
    grab(h, 1, "touch");
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 400 });
    fireEvent.pointerDown(h, {
      pointerId: 2,
      pointerType: "touch",
      clientX: 400,
    });
    fireEvent.pointerMove(h, { pointerId: 2, clientX: 300 });
    fireEvent.pointerUp(h, { pointerId: 2 });
    expect(pref()).toBe("400px"); // finger 2 moved nothing
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 430 });
    fireEvent.pointerUp(h, { pointerId: 1 });
    expect(valuenow()).toBe(430);
    expect(stored()).toBe("430");
  });

  // preventDefault on pointerdown kills the compatibility mousedown, and with it
  // the focus it would have given us.
  it("focuses the handle on grab, so the arrow keys work straight after", () => {
    render(<RailResizer />);
    const h = handle();
    fireEvent.pointerDown(h, {
      pointerId: 1,
      pointerType: "mouse",
      button: 0,
    });
    expect(document.activeElement).toBe(h);
  });

  it("resets on a double tap", () => {
    const clock = vi.spyOn(performance, "now");
    localStorage.setItem("rongo.rail-width", "480");
    render(<RailResizer />);
    const h = handle();
    clock.mockReturnValue(1000);
    fireEvent.pointerDown(h, { pointerId: 1, pointerType: "touch" });
    fireEvent.pointerUp(h, { pointerId: 1 });
    clock.mockReturnValue(1200);
    fireEvent.pointerDown(h, { pointerId: 1, pointerType: "touch" });
    expect(valuenow()).toBe(RAIL_DEFAULT);
    expect(stored()).toBe(String(RAIL_DEFAULT));
  });

  // Review finding: the arrow keys stepped the preference while the drag stepped
  // the rendered edge, so above the 40vw cap the first presses moved nothing on
  // screen. The width is computed from the viewport now, never measured.
  it("narrows from the edge the user can see, not from a hidden preference", () => {
    withViewport(1100); // 40vw = 440, so a stored 520 shows as 440
    localStorage.setItem("rongo.rail-width", "520");
    render(<RailResizer />);

    fireEvent.keyDown(handle(), { key: "ArrowLeft" });
    expect(valuenow()).toBe(424); // 440 - 16, the edge that actually moved
  });

  // The other direction: widening must not be capped away, or a preference above
  // the cap could never grow and the press would look dead.
  it("widens the preference even while the cap holds the edge back", () => {
    withViewport(1100);
    localStorage.setItem("rongo.rail-width", "460");
    render(<RailResizer />);

    fireEvent.keyDown(handle(), { key: "ArrowRight" });
    expect(stored()).toBe("476"); // 460 + 16, though the edge stays at 440
  });

  it("mirrors the CSS clamp it stands in for", () => {
    withViewport(1600); // 40vw = 640, so the 520 max wins
    expect(displayedWidth(520)).toBe(520);
    expect(displayedWidth(200)).toBe(RAIL_MIN);
    withViewport(1100); // 40vw = 440 wins over the max
    expect(displayedWidth(520)).toBe(440);
    withViewport(600); // 40vw = 240, below the min, so the min wins
    expect(displayedWidth(400)).toBe(RAIL_MIN);
  });

  // Review finding: body.resizing had no unmount cleanup, and it disables the
  // cursor and selection page-wide.
  it("clears the resizing class if it is unmounted mid-drag", () => {
    const view = render(<RailResizer />);
    grab(handle());
    expect(document.body.classList.contains("resizing")).toBe(true);
    view.unmount();
    expect(document.body.classList.contains("resizing")).toBe(false);
  });

  // Review finding: a drag on a capped viewport threw the wider preference away.
  // The first fix kept storage but still painted the capped number, which left
  // two live widths for one preference. Assert all three move together.
  it("grows the preference from a capped drag, in storage and in the paint", () => {
    withViewport(1100); // 40vw = 440; the stored 520 is held back on screen
    localStorage.setItem("rongo.rail-width", "520");
    render(<RailResizer />);
    const h = handle();

    fireEvent.pointerDown(h, {
      pointerId: 1,
      pointerType: "mouse",
      button: 0,
      clientX: 440,
    });
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 465 }); // +25
    fireEvent.pointerUp(h, { pointerId: 1 });

    // 520 + 25 runs into the 520 max, so the preference stays 520 — and is NOT
    // cut down to the 440 the viewport was showing.
    expect(stored()).toBe(String(RAIL_MAX));
    expect(pref()).toBe(RAIL_MAX + "px"); // one number, not two
    // The separator sits at the capped edge, so that is what it announces:
    // saying 520 would describe a rail that is not on screen.
    expect(valuenow()).toBe(440);
  });

  // Uncapped: a deliberate narrowing is taken at face value. The capped case is
  // its own test below, because that is where the two numbers diverge.
  it("lowers the stored preference when the user narrows", () => {
    localStorage.setItem("rongo.rail-width", "520");
    render(<RailResizer />);
    const h = handle();
    fireEvent.pointerDown(h, {
      pointerId: 1,
      pointerType: "mouse",
      button: 0,
      clientX: 520,
    });
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 400 });
    fireEvent.pointerUp(h, { pointerId: 1 });
    expect(stored()).toBe("400");
  });

  // Review finding: travel was always added to the preference, so under the cap a
  // narrowing drag had a dead zone as wide as the gap between the two. Pulling the
  // edge 40px left moved nothing on screen AND wrote 480 back: the narrowing was
  // neither honoured nor preserved.
  it("narrows from the first pixel when the cap is holding the edge back", () => {
    withViewport(1100); // cap 440, so a stored 520 shows as 440
    localStorage.setItem("rongo.rail-width", "520");
    render(<RailResizer />);
    const h = handle();

    fireEvent.pointerDown(h, {
      pointerId: 1,
      pointerType: "mouse",
      button: 0,
      clientX: 440, // the edge the user can see
    });
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 400 }); // pull 40px left
    expect(pref()).toBe("400px"); // tracked the pointer, no dead zone
    fireEvent.pointerUp(h, { pointerId: 1 });
    expect(stored()).toBe("400"); // and the narrowing was kept
  });

  // The same drag the other way still protects a preference above the cap.
  it("still keeps a capped preference when the drag widens", () => {
    withViewport(1100);
    localStorage.setItem("rongo.rail-width", "520");
    render(<RailResizer />);
    const h = handle();

    fireEvent.pointerDown(h, {
      pointerId: 1,
      pointerType: "mouse",
      button: 0,
      clientX: 440,
    });
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 460 });
    fireEvent.pointerUp(h, { pointerId: 1 });
    expect(stored()).toBe(String(RAIL_MAX)); // 520 + 20, clamped, not cut to 440
  });

  // Review finding: aria-valuenow reported the preference while the separator sat
  // at the capped edge, so a screen reader was told about a rail that is not there.
  it("announces where the separator actually is", () => {
    withViewport(1100);
    localStorage.setItem("rongo.rail-width", "520");
    render(<RailResizer />);
    expect(valuenow()).toBe(440);
    expect(handle().getAttribute("aria-valuemax")).toBe("440");
  });

  // Review finding: aria-valuenow is computed from the viewport, but CSS repaints
  // the edge on a resize while React does not re-render, so the separator went on
  // announcing the width from the last render — wrong as the preference and wrong
  // as the edge at once.
  it("re-announces the edge when the window changes size", async () => {
    withViewport(1600); // cap is the plain 520 max
    localStorage.setItem("rongo.rail-width", "520");
    render(<RailResizer />);
    expect(valuenow()).toBe(520);

    withViewport(1100); // 40vw = 440 now binds
    fireEvent(window, new Event("resize"));
    await waitFor(() => expect(valuenow()).toBe(440));
    expect(handle().getAttribute("aria-valuemax")).toBe("440");

    withViewport(1600); // and back
    fireEvent(window, new Event("resize"));
    await waitFor(() => expect(valuenow()).toBe(520));
  });

  // Regression: the handle was moved to 1px inside the border to stop it covering
  // the scrollbar, which put 9 of its 10px over the content. A double-click 2px
  // past the border then hit the double-tap branch and silently reset a stored
  // width to the default. The strip has to stay mostly on the rail's side.
  it("keeps all but a few pixels of the strip off the content", () => {
    render(<RailResizer />);
    const left = handle().style.left;
    // calc(var(--rail-w) - Npx): N is how far the strip reaches back over the
    // rail, and 10px wide means 10 - N hangs over the content.
    const inset = Number(/-\s*(\d+)px/.exec(left)?.[1]);
    expect(inset).toBeGreaterThanOrEqual(7); // at most 3px over the content
  });

  // The reset is the tablet's only way back to the default, so it is a TOUCH
  // gesture. A mouse has the arrow keys and Home, and letting it reset meant an
  // ordinary double-click near the border threw a stored width away: the strip
  // straddles the border, so the click need not even be aimed at the handle.
  it("does not reset on a mouse double-click", () => {
    const clock = vi.spyOn(performance, "now");
    localStorage.setItem("rongo.rail-width", "480");
    render(<RailResizer />);
    const h = handle();

    clock.mockReturnValue(1000);
    fireEvent.pointerDown(h, { pointerId: 1, pointerType: "mouse", button: 0 });
    fireEvent.pointerUp(h, { pointerId: 1 });
    clock.mockReturnValue(1100); // well inside the tap window
    fireEvent.pointerDown(h, { pointerId: 1, pointerType: "mouse", button: 0 });
    fireEvent.pointerUp(h, { pointerId: 1 });

    expect(valuenow()).toBe(480); // the rail width survives the double-click
    expect(localStorage.getItem("rongo.rail-width")).toBe("480");
  });

  // A finger still gets its reset, which is the whole reason the gesture exists.
  it("still resets on a touch double tap", () => {
    const clock = vi.spyOn(performance, "now");
    localStorage.setItem("rongo.rail-width", "480");
    render(<RailResizer />);
    const h = handle();

    clock.mockReturnValue(1000);
    fireEvent.pointerDown(h, { pointerId: 1, pointerType: "touch" });
    fireEvent.pointerUp(h, { pointerId: 1 });
    clock.mockReturnValue(1150);
    fireEvent.pointerDown(h, { pointerId: 1, pointerType: "touch" });

    expect(valuenow()).toBe(RAIL_DEFAULT);
  });

  // Review finding: a cancel before the slop left the double-tap window armed, so
  // the next deliberate grab inside 350ms was swallowed as a reset.
  it("does not let a cancelled tap arm the double-tap reset", () => {
    const clock = vi.spyOn(performance, "now");
    localStorage.setItem("rongo.rail-width", "480");
    render(<RailResizer />);
    const h = handle();

    clock.mockReturnValue(1000);
    fireEvent.pointerDown(h, {
      pointerId: 1,
      pointerType: "touch",
      clientX: 480,
    });
    fireEvent.pointerCancel(h, { pointerId: 1 }); // system took the pointer

    clock.mockReturnValue(1100); // inside the tap window
    fireEvent.pointerDown(h, {
      pointerId: 1,
      pointerType: "touch",
      clientX: 480,
    });
    fireEvent.pointerMove(h, { pointerId: 1, clientX: 450 });
    fireEvent.pointerUp(h, { pointerId: 1 });
    expect(valuenow()).toBe(450); // dragged, not reset to 362
  });

  it("does not reset two slow taps", () => {
    const clock = vi.spyOn(performance, "now");
    localStorage.setItem("rongo.rail-width", "480");
    render(<RailResizer />);
    const h = handle();
    clock.mockReturnValue(1000);
    fireEvent.pointerDown(h, { pointerId: 1, pointerType: "touch" });
    fireEvent.pointerUp(h, { pointerId: 1 });
    clock.mockReturnValue(3000);
    fireEvent.pointerDown(h, { pointerId: 1, pointerType: "touch" });
    fireEvent.pointerUp(h, { pointerId: 1 });
    expect(valuenow()).toBe(480);
  });
});
