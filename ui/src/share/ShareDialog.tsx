import { useEffect, useRef, useState } from "react";

import { ModalShell, cancelButton, saveButton } from "../ThreadModals";
import { conflict, createShare, revokeShare, shareURL, updateShare, type Share } from "./api";

/**
 * Where a thread is handed out, and taken back.
 *
 * Four states, and the difference between them is what the thread has done
 * since the link was made. Not yet shared: one button. Shared: the link and a
 * way to copy it. Shared, and questions asked since: an ochre line offering to
 * raise the ceiling — ochre because that is the reader's move, and the link
 * itself does not change when they make it.
 *
 * Revoke asks nothing back. Unlike Delete it is undoable — sharing again hands
 * back the same link — so a confirmation would be theatre. It is quiet red
 * rather than the solid danger fill Delete wears, for the same reason.
 */
export default function ShareDialog({
  threadID,
  title,
  share: initial,
  onCancel,
  onChange,
}: {
  threadID: number;
  title: string;
  /** The link this thread already has, or null. */
  share: Share | null;
  onCancel: () => void;
  /** A link was made, moved or taken back. */
  onChange: (share: Share | null) => void;
}) {
  const [share, setShare] = useState<Share | null>(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState(false);
  // Cleared on unmount: the dialog closes well inside 1500 ms, and a timer
  // firing into a gone component is a warning in the console and a leak.
  const copyTimer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(copyTimer.current), []);

  async function run(call: () => Promise<Share | number>) {
    setBusy(true);
    setError("");
    try {
      const got = await call();
      if (typeof got === "number") {
        setError(
          got === conflict
            ? "The last turn is still being answered. Try again once it has finished."
            : "That did not work. Try again in a moment.",
        );
        return;
      }
      setShare(got);
      onChange(got);
    } catch {
      setError("The connection was lost.");
    } finally {
      setBusy(false);
    }
  }

  async function revoke() {
    setBusy(true);
    setError("");
    try {
      if (!(await revokeShare(threadID))) {
        setError("The link could not be taken back. Try again in a moment.");
        return;
      }
      setShare(null);
      onChange(null);
    } catch {
      setError("The connection was lost.");
    } finally {
      setBusy(false);
    }
  }

  async function copy() {
    if (!share) return;
    try {
      await navigator.clipboard.writeText(shareURL(share));
      setCopied(true);
      copyTimer.current = window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // No fallback: execCommand is gone, and the URL is right there to
      // select. Saying so beats a button that silently does nothing.
      setError("The link could not be copied. Select it and copy it by hand.");
    }
  }

  const when = share ? new Date(share.updated_at) : null;
  const frozen =
    when && !Number.isNaN(when.getTime())
      ? when.toLocaleString("en-GB", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })
      : "";

  return (
    <ModalShell title={share ? "Thread shared" : "Share thread"} onCancel={onCancel}>
      <p className="mt-3 text-sm/6 text-muted">
        {share ? (
          <>
            Anyone with the link can read “{title}” as it stood{frozen ? ` at ${frozen}` : " when it was made"}.
            Questions asked later are not included.
          </>
        ) : (
          <>
            Anyone with the link can read “{title}” as it stands now, without signing in. Questions asked
            later are not included.
          </>
        )}
      </p>

      {share && share.newer > 0 && (
        // Ochre is "your move": the thread has moved on and the link has not,
        // and only the owner can decide whether it should follow.
        <div className="mt-3.5 flex items-center gap-3 rounded-ui-sm border border-ochre bg-ochre-wash px-3 py-2.5">
          <p className="m-0 flex-1 text-[13px] text-ochre">
            {share.newer === 1
              ? "1 question has been asked since this link was made. It is not on it."
              : `${share.newer} questions have been asked since this link was made. They are not on it.`}
          </p>
          <button
            type="button"
            disabled={busy}
            onClick={() => void run(() => updateShare(threadID))}
            className="h-8 shrink-0 rounded-ui-sm bg-ochre px-3 text-sm font-medium text-bg transition-colors hover:brightness-110 disabled:opacity-50"
          >
            Update link
          </button>
        </div>
      )}

      {share && (
        <div className="mt-3.5 flex items-center gap-2.5 rounded-ui-sm border border-border bg-bg py-1.5 pr-1.5 pl-3">
          {/* The URL runs out under a mask rather than an ellipsis, so it
              never collides with the button beside it. */}
          <span
            className="min-w-0 flex-1 overflow-hidden font-mono text-xs whitespace-nowrap text-muted"
            style={{ maskImage: "linear-gradient(to right, #000 82%, transparent)" }}
          >
            {shareURL(share)}
          </span>
          <button type="button" onClick={() => void copy()} className={saveButton + " shrink-0"}>
            {copied ? "Copied" : "Copy link"}
          </button>
        </div>
      )}

      {error && (
        <p role="alert" className="mt-3 text-sm text-accent-strong">
          {error}
        </p>
      )}

      <p className="mt-3 text-xs/5 text-faint">
        The answers, the sources they cite and the files behind them are readable without signing in.
        Token usage and costs are not shared.
      </p>

      <div className="mt-4 flex items-center justify-end gap-2">
        {share && (
          <button
            type="button"
            disabled={busy}
            onClick={() => void revoke()}
            className="mr-auto h-8 rounded-ui-sm px-3.5 text-sm font-medium text-danger transition-colors hover:bg-elevated disabled:opacity-50"
          >
            Revoke link
          </button>
        )}
        <button type="button" className={cancelButton} onClick={onCancel}>
          {share ? "Done" : "Cancel"}
        </button>
        {!share && (
          <button
            type="button"
            disabled={busy}
            onClick={() => void run(() => createShare(threadID))}
            className={saveButton}
          >
            Create link
          </button>
        )}
      </div>
    </ModalShell>
  );
}
