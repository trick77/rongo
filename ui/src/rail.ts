/**
 * The rail's two type sizes, ../loom's metrics. Kept here because the rows
 * are painted in two files: the actions at the top of the rail in App, the
 * history and its "All threads" foot in Threads.
 */

/**
 * A rail button: 26px tall, 14/20 text, 6px radius — loom's SidebarItems.tsx
 * has `h-[26px] rounded-md px-1.5 gap-2.5`. rounded-md rather than the app's
 * own rounded-ui-sm because the rail is loom's surface and 8px reads rounder
 * than loom's rows do; rounded-ui-sm stays for everything else.
 */
export const railRow =
  "flex h-[26px] w-full items-center gap-2.5 rounded-md px-1.5 text-left text-sm/5 " +
  "disabled:opacity-50";

/**
 * The rail's label size: 12/16 in sentence case, not an uppercase tracked
 * eyebrow. The day groups in Threads are the only thing wearing it — the rail
 * has no heading of its own, the titles stand alone.
 */
export const railLabel = "px-1.5 text-xs/4 text-rail-label";
