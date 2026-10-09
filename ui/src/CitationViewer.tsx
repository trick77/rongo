import CommitView from "./CommitView";
import SourceView, { isCommit, type SourceRef } from "./SourceView";

/**
 * CitationViewer opens the overlay a citation asks for: a commit citation
 * opens the commit view, whose touched files open the source viewer in turn,
 * whole, at their indexed commit; a file citation opens the source viewer.
 * The share page passes its own endpoints, which serve only what the link
 * cites, and no onOpenFile: a commit's touched files are not citations.
 */
export default function CitationViewer({
  viewing,
  onClose,
  onOpenFile,
  endpoints,
}: {
  viewing: SourceRef | null;
  onClose: () => void;
  onOpenFile?: (ref: SourceRef) => void;
  endpoints?: { source: string; commit: string };
}) {
  if (!viewing) return null;
  if (isCommit(viewing)) {
    return <CommitView source={viewing} endpoint={endpoints?.commit} onClose={onClose} onOpenFile={onOpenFile} />;
  }
  return <SourceView source={viewing} endpoint={endpoints?.source} onClose={onClose} />;
}
