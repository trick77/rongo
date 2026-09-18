import type { Route } from "./routing";

/**
 * What the browser tab says, ../loom's tabTitle with rongo's pages. Before
 * this every tab read "Rongo", and five of them open on five threads were
 * five identical tabs.
 *
 * A thread contributes only its settled title — the caller hands null while
 * the model's title call is pending, for the same reason the header does: the
 * placeholder is the question's first 48 runes cut mid-word, and a tab is
 * narrower still. Until then the tab says "Rongo" and nothing about a thread.
 *
 * The share page is never rendered inside the app (see main.tsx) and sets
 * document.title itself once the share has loaded.
 */
export function tabTitle(route: Route, openTitle: string | null): string {
  let base: string | null;
  switch (route.view) {
    case "new":
      base = "New question";
      break;
    case "threads":
      base = "Threads";
      break;
    case "projects":
      base = "Projects";
      break;
    case "shared":
      base = "Shared";
      break;
    case "memory":
      base = "Memory";
      break;
    case "thread":
      base = openTitle;
      break;
    case "share":
      base = null;
      break;
    default: {
      // Exhaustive: a new Route view is a compile error here, not a tab that
      // silently keeps the previous page's name.
      const _exhaustive: never = route;
      return _exhaustive;
    }
  }
  return base !== null ? `${base} · Rongo` : "Rongo";
}
