/**
 * The reader's standing instructions: one read for the page, one delete for
 * the page's × and for the undo under an answer. Nothing writes here — a rule
 * is given in chat, where the question is understood.
 */
export type Memory = {
  id: number;
  text: string;
  /** The project or repository the rule is limited to; absent is everywhere. */
  scope?: string;
  /** False when the scope no longer names anything indexed: the rule then
   * holds everywhere, and the page says so. */
  scope_live: boolean;
  /** The thread the rule was said in, while that turn exists. */
  thread_id?: string;
  created_at: string;
};

export type MemoryPage = {
  /** False on a deployment that keeps no memory (BACKEND_MEMORY=false). */
  enabled: boolean;
  memories: Memory[];
};

export async function listMemories(): Promise<MemoryPage> {
  const res = await fetch("/api/memory");
  if (!res.ok) throw new Error(`memory: ${res.status}`);
  const page = (await res.json()) as MemoryPage;
  return { enabled: page.enabled === true, memories: Array.isArray(page.memories) ? page.memories : [] };
}

/** True when the rule is gone, whether this call removed it or it already
 * was: both are what the reader asked for. */
export async function forgetMemory(id: number): Promise<boolean> {
  const res = await fetch(`/api/memory/${id}`, { method: "DELETE" });
  return res.ok || res.status === 404;
}
