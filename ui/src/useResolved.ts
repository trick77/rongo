import { useEffect, useState } from "react";

/**
 * useResolved is what load(key) resolves to, null while it is on its way and
 * again from the moment the key changes. An answer that arrives for a key the
 * component has moved on from is dropped.
 *
 * load must not reject, and must be the same function between renders: both
 * drawings that use this (Mermaid, PlantUML) are module functions that turn a
 * failure into a value.
 */
export function useResolved<T>(key: string, load: (key: string) => Promise<T>): T | null {
  const [out, setOut] = useState<T | null>(null);
  useEffect(() => {
    let live = true;
    setOut(null);
    load(key).then((d) => {
      if (live) setOut(d);
    });
    return () => {
      live = false;
    };
  }, [key, load]);
  return out;
}
