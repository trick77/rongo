/** Readers of the trace wire format, the server's untyped step details: a
 * field that is not the expected shape reads as empty, never as a crash. */
export const str = (v: unknown): string => (typeof v === "string" ? v : "");
export const num = (v: unknown): number => (typeof v === "number" ? v : 0);
export const strs = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : []);
