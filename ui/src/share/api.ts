/**
 * The four things an owner can do to a link, and the one read that lists them.
 *
 * The server returns a PATH, never an absolute URL: rongo behind a
 * TLS-terminating proxy only ever sees plain HTTP and would build the wrong
 * one. The browser is the only thing that knows its own origin, so it puts it
 * in front here.
 */
export type Share = {
  token: string;
  path: string;
  thread_id: number;
  title: string;
  up_to_message_id: number;
  /** Turns the link covers, and turns asked since it was made. */
  turns: number;
  newer: number;
  shared_at: string;
  updated_at: string;
};

export function shareURL(share: Share): string {
  return window.location.origin + share.path;
}

/** ErrUnfinished's answer: the last turn is still being written. */
export const conflict = 409;

async function shareCall(method: string, path: string): Promise<Share | number> {
  const res = await fetch(path, { method });
  if (!res.ok) return res.status;
  return (await res.json()) as Share;
}

export function createShare(threadID: number) {
  return shareCall("POST", `/api/threads/${threadID}/share`);
}

export function updateShare(threadID: number) {
  return shareCall("POST", `/api/threads/${threadID}/share/update`);
}

/** The live link on a thread, or null when it has none. */
export async function shareFor(threadID: number): Promise<Share | null> {
  const list = await listShares();
  return list.find((s) => s.thread_id === threadID) ?? null;
}

export async function revokeShare(threadID: number): Promise<boolean> {
  const res = await fetch(`/api/threads/${threadID}/share`, { method: "DELETE" });
  // A 404 is a link that is already gone, which is what was asked for.
  return res.ok || res.status === 404;
}

export async function listShares(): Promise<Share[]> {
  const res = await fetch("/api/shares");
  if (!res.ok) return [];
  const list = await res.json();
  return Array.isArray(list) ? list : [];
}
