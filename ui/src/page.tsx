import type { ReactNode } from "react";

/** pageColumn is the one measure every page is laid out in: 900px, centred.
 * One cap and one centring keeps the pages' first lines on a single rule;
 * a page that carried its own would drift the moment one was edited. The
 * padding differs per page (a page with a composer clears its fades), so
 * only the column is shared. */
export const pageColumn = "mx-auto max-w-[900px]";

/** PageShell is the column with a title and an optional intro, the shape of
 * every page but the thread and the share. leading-tight like Ask's welcome
 * heading: without it the taller line box puts the title 3px below the other
 * page's. */
export function PageShell({ title, intro, children }: { title: string; intro?: ReactNode; children: ReactNode }) {
  return (
    <div className="h-full overflow-auto">
      <div className={`${pageColumn} px-4 py-6 sm:px-6 lg:px-10 lg:py-8`}>
        <h2
          className={
            "font-serif text-[22px] font-medium leading-tight tracking-tight text-ink sm:text-[28px]" +
            (intro ? "" : " mb-6")
          }
        >
          {title}
        </h2>
        {intro && <p className="mt-1 mb-6 text-[14.5px] text-muted">{intro}</p>}
        {children}
      </div>
    </div>
  );
}
