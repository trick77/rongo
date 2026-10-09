import { useCallback, useEffect, useRef, useState, type Dispatch, type SetStateAction } from "react";
import { Refused } from "./http";

/** What a view has of the thing it reads: nothing yet, why it has nothing, or
 * the thing. */
export type Loaded<T> = { state: "loading" } | { state: "error"; message: string } | ({ state: "ready" } & T);

/**
 * useLoaded reads one thing for a view and keeps it: an overlay's file or
 * commit, a page's list. load runs when deps change, and a result landing
 * after they did (or after the view closed) is dropped rather than written
 * over whatever is there now. A server that answered and refused gives its
 * own message (Refused, from fetchJSON); anything else that fails is the
 * connection. The setter is for a page that takes rows off its list as the
 * reader acts.
 */
export function useLoaded<T extends object>(
  load: () => Promise<T>,
  deps: unknown[],
): [Loaded<T>, Dispatch<SetStateAction<Loaded<T>>>] {
  const [loaded, setLoaded] = useState<Loaded<T>>({ state: "loading" });
  useEffect(() => {
    let cancelled = false;
    setLoaded({ state: "loading" });
    (async () => {
      try {
        const value = await load();
        if (!cancelled) setLoaded({ state: "ready", ...value });
      } catch (err) {
        if (cancelled) return;
        setLoaded({ state: "error", message: err instanceof Refused ? err.message : "The connection was lost." });
      }
    })();
    return () => {
      cancelled = true;
    };
    // load is a closure over what deps name; keying on it would re-read on every render.
  }, deps);
  return [loaded, setLoaded];
}

/** useReportCount tells the header how many rows there are, and withdraws
 * the number with the list: a pill saying "0" over a list that failed to
 * load would be a claim. */
export function useReportCount(count: number | null, onCount: (n: number | null) => void): void {
  useEffect(() => {
    onCount(count);
    return () => onCount(null);
    // The count is the fact; the callback is the parent's stable setter.
  }, [count]);
}

/** useFlash holds a value for a moment, the way "Copied" sits on a button,
 * and lets go of it only while it is still the same one: flashing a second
 * row before the first has faded must not take the label off the new one.
 * Cleared on unmount, so a reader who leaves within the moment does not have
 * state set on a view that is gone. The third element lets go of it now, for
 * a view whose indices mean something else after a change. */
export function useFlash<T>(ms = 1500): [T | null, (v: T) => void, () => void] {
  const [value, setValue] = useState<T | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  const flash = useCallback(
    (v: T) => {
      setValue(v);
      clearTimeout(timer.current);
      timer.current = setTimeout(() => setValue((cur) => (cur === v ? null : cur)), ms);
    },
    [ms],
  );
  const clear = useCallback(() => {
    clearTimeout(timer.current);
    setValue(null);
  }, []);
  return [value, flash, clear];
}

/**
 * useOpenUntil is the fold of a card that is "your move": open until decided,
 * shut the instant it is, and open again if the decision is taken back
 * because the turn it started failed. Only on the transition, never on a
 * re-render that leaves decided unchanged, so a reader who toggles it by hand
 * is never fought. reopen false is the fold of a record that only ever shuts.
 */
export function useOpenUntil(decided: boolean, reopen = true): [boolean, Dispatch<SetStateAction<boolean>>] {
  const [open, setOpen] = useState(!decided);
  const was = useRef(decided);
  useEffect(() => {
    if (!was.current && decided) setOpen(false);
    if (reopen && was.current && !decided) setOpen(true);
    was.current = decided;
  }, [decided, reopen]);
  return [open, setOpen];
}
