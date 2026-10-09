/** One row of a list, changed in place; the rest untouched. */
export const patch =
  <T extends { id: string }>(id: string, change: Partial<T>) =>
  (prev: T[]): T[] =>
    prev.map((x) => (x.id === id ? { ...x, ...change } : x));
