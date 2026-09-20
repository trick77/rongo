import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render } from "@testing-library/react";
import {
  RAIL_DEFAULT,
  RAIL_MAX,
  RAIL_MIN,
  RailResizer,
  clampRail,
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

beforeEach(() => {
  localStorage.clear();
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
