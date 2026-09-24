/**
 * The URL is where the app is, ../loom's routing.ts with rongo's own pages.
 *
 * Before this, the open thread lived in localStorage and every page looked the
 * same from the address bar: a thread could not be sent to anyone, reloaded
 * into, or reached with Back. The bookmark is gone with it — two places
 * remembering which thread is open is two places to disagree the moment
 * someone presses Back.
 *
 * "/" is not a route. It replaceStates to /new on the first render, as loom
 * does, so the address bar always says what is on screen.
 */
export type Route =
  | { view: "new" }
  | { view: "thread"; id: string }
  /** Every thread, searchable: what the rail's page of 30 is a cut of. */
  | { view: "threads" }
  | { view: "projects" }
  | { view: "shared" }
  /** The reader's standing instructions. */
  | { view: "memory" }
  /** The public page. Never rendered inside the app — see main.tsx. */
  | { view: "share"; token: string };

export const sharePrefix = "/share/";
const threadPrefix = "/thread/";

// decoded is the percent-decoded rest of a path, or null where it is not
// valid percent-encoding at all ("/thread/%E0"). decodeURIComponent throws
// on that, and it runs at module load: unguarded, one bad link rendered
// nothing, not even the shell.
function decoded(s: string): string | null {
  try {
    return decodeURIComponent(s);
  } catch {
    return null;
  }
}

export function routeFromPath(path: string): Route {
  if (path.startsWith(threadPrefix)) {
    // The exact shape the server mints — 22 URL-safe characters, 128 bits —
    // and nothing else. Anything that is not one is not a thread, and lands on
    // the unasked question rather than becoming a fetch of a thread that
    // cannot exist: a request the backend answers 404, which Ask reads as "not
    // right now" rather than as a dead thread, leaving a blank column and that
    // URL in the bar with no way back but the rail.
    //
    // /thread/19 is rejected by this too, which is the point. Threads were
    // addressed by row number for one release; those URLs are not redirected,
    // because a redirect would keep the counter reachable for good.
    const raw = decoded(path.slice(threadPrefix.length));
    if (raw !== null && /^[A-Za-z0-9_-]{22}$/.test(raw)) return { view: "thread", id: raw };
  }
  if (path.startsWith(sharePrefix)) {
    const token = decoded(path.slice(sharePrefix.length));
    if (token !== null && token !== "") return { view: "share", token };
  }
  if (path === "/threads") return { view: "threads" };
  if (path === "/projects") return { view: "projects" };
  if (path === "/shared") return { view: "shared" };
  if (path === "/memory") return { view: "memory" };
  return { view: "new" };
}

export function routeFromLocation(): Route {
  // Defaulted rather than assumed: a stubbed location in a test, and a
  // document with no path at all, must land on "new" instead of throwing
  // before the app has rendered anything.
  return routeFromPath(window.location.pathname ?? "/");
}

export function pathForRoute(route: Route): string {
  switch (route.view) {
    case "thread":
      return threadPrefix + encodeURIComponent(route.id);
    case "threads":
      return "/threads";
    case "projects":
      return "/projects";
    case "shared":
      return "/shared";
    case "memory":
      return "/memory";
    case "share":
      return sharePrefix + encodeURIComponent(route.token);
    default:
      return "/new";
  }
}

/**
 * Moves to a route. Guarded on the current path so that landing on a thread
 * from its own URL does not push the same entry a second time — Back would
 * then need two presses to leave it.
 *
 * `replace` is for the app CORRECTING an address the reader did not choose: a
 * thread that turns out to be deleted or not theirs, and the one they just
 * deleted themselves. Pushed instead, Back would return to that dead address,
 * the correction would push again, and Back could never leave the app.
 */
export function navigate(route: Route, replace = false) {
  const path = pathForRoute(route);
  if (window.location.pathname === path) return;
  if (replace) window.history.replaceState({}, "", path);
  else window.history.pushState({}, "", path);
}
