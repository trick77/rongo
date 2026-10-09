import { useState } from "react";

import { copyText } from "../http";
import { useFlash, useLoaded, useReportCount } from "../hooks";
import { Icon } from "../Icon";
import { listShares, revokeShare, shareURL, type Share } from "./api";

/**
 * Every live link this reader owns, in one place.
 *
 * The audit view: a link handed out months ago must not be something you have
 * to open every thread to find. A revoked link is not listed — it is not a
 * link any more, and keeping it as a greyed row would turn the one place that
 * answers "what is out there" into a history of what used to be.
 *
 * Raising a link's ceiling is deliberately NOT here. "3 turns newer" is a
 * badge; the place to act on it is the thread's own dialog, where the reader
 * can see the turns that would be added.
 */
const th = "px-3.5 py-2.5 text-[11px] font-medium uppercase tracking-[.09em] text-faint";
const action =
  "ml-1.5 inline-flex h-7 items-center gap-1.5 rounded-full border border-border bg-panel px-3 " +
  "text-[13px] text-ink-dim transition-colors hover:border-elevated-border hover:bg-active disabled:opacity-50";

function day(iso: string): { date: string; ago: string } {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return { date: "", ago: "" };
  const days = Math.round((Date.now() - d.getTime()) / 86400000);
  return {
    date: d.toLocaleString("en-GB", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }),
    ago: days <= 0 ? "today" : days === 1 ? "yesterday" : `${days} days ago`,
  };
}

export default function SharedLinks({
  onChange = () => {},
  onCount = () => {},
  onOpenThread,
}: {
  /** A link was taken back, so the rail's markers are stale. */
  onChange?: () => void;
  /** How many links there are, for the header's pill; null until known. */
  onCount?: (n: number | null) => void;
  onOpenThread: (id: string) => void;
}) {
  const [state, setState] = useLoaded(async () => ({ shares: await listShares() }), []);
  const [copied, flashCopied] = useFlash<string>();
  const [busy, setBusy] = useState<string | null>(null);

  useReportCount(state.state === "ready" ? state.shares.length : null, onCount);

  async function copy(share: Share) {
    // Nothing said when the clipboard refused: the URL is on the row to
    // select, and the row already shows it.
    if (await copyText(shareURL(share))) flashCopied(share.token);
  }

  async function revoke(share: Share) {
    setBusy(share.thread_id);
    try {
      if (!(await revokeShare(share.thread_id))) return;
      setState((prev) =>
        prev.state === "ready" ? { ...prev, shares: prev.shares.filter((x) => x.token !== share.token) } : prev,
      );
      onChange();
    } catch {
      // The row stays: telling someone their link is gone when it is not is
      // worse than saying nothing.
    } finally {
      setBusy(null);
    }
  }

  if (state.state === "loading") return <p className="text-muted">Loading…</p>;
  if (state.state === "error")
    return (
      <p role="alert" className="text-accent-strong">
        The shared links cannot be fetched.
      </p>
    );
  if (state.shares.length === 0)
    return (
      <p className="text-muted">
        No thread is shared. A thread's{" "}
        <Icon name="moreVertical" size="15px" label="actions" className="align-[-2px]" /> menu in the sidebar
        has <span className="font-medium text-ink-dim">Share</span>.
      </p>
    );

  return (
    <div className="overflow-x-auto overscroll-x-contain rounded-ui border border-border">
      <table className="w-full min-w-[720px] border-separate border-spacing-0 bg-panel text-sm">
        <thead>
          <tr className="bg-bg">
            <th className={th + " border-b border-border text-left"}>Thread</th>
            <th className={th + " border-b border-border text-left"}>Frozen at</th>
            <th className={th + " border-b border-border text-right"}>Turns</th>
            <th className={th + " border-b border-border text-left"}>State</th>
            <th className={th + " border-b border-border"} />
          </tr>
        </thead>
        <tbody>
          {state.shares.map((share) => {
            const when = day(share.updated_at);
            return (
              <tr key={share.token} className="align-top [&>td]:border-b [&>td]:border-border-soft last:[&>td]:border-b-0">
                <td className="px-3.5 py-3">
                  <button
                    type="button"
                    onClick={() => onOpenThread(share.thread_id)}
                    className="text-left font-medium text-ink underline-offset-[3px] hover:underline hover:decoration-accent"
                  >
                    {share.title}
                  </button>
                  <span className="mt-0.5 block font-mono text-[11.5px] break-all text-faint">{share.path}</span>
                </td>
                <td className="px-3.5 py-3 whitespace-nowrap text-muted">
                  {when.date}
                  <span className="block text-xs text-faint">{when.ago}</span>
                </td>
                <td className="px-3.5 py-3 text-right font-mono tabular-nums text-muted">{share.turns}</td>
                <td className="px-3.5 py-3">
                  {share.newer > 0 ? (
                    <span className="rounded-full bg-ochre-wash px-2.5 py-0.5 text-xs font-medium text-ochre">
                      {share.newer} {share.newer === 1 ? "turn" : "turns"} newer
                    </span>
                  ) : (
                    <span className="rounded-full bg-accent-dim px-2.5 py-0.5 text-xs font-medium text-accent-strong">
                      Live
                    </span>
                  )}
                </td>
                <td className="px-3.5 py-3 text-right whitespace-nowrap">
                  <button type="button" onClick={() => void copy(share)} className={action}>
                    <Icon name="upload" size="14px" />
                    {copied === share.token ? "Copied" : "Copy link"}
                  </button>
                  <button
                    type="button"
                    disabled={busy === share.thread_id}
                    onClick={() => void revoke(share)}
                    className={action + " text-danger"}
                  >
                    <Icon name="eyeOff" size="14px" />
                    Revoke
                  </button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
