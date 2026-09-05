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
  | { view: "thread"; id: number }
  | { view: "repos" }
  | { view: "shared" }
  /** The public page. Never rendered inside the app — see main.tsx. */
  | { view: "share"; token: string };

export const sharePrefix = "/share/";
const threadPrefix = "/thread/";

export function routeFromPath(path: string): Route {
  if (path.startsWith(threadPrefix)) {
    // Number(), then a finite check: "/thread/abc" is not a thread, and
    // NaN would be carried all the way to a fetch of /api/threads/NaN.
    const id = Number(decodeURIComponent(path.slice(threadPrefix.length)));
    if (Number.isFinite(id) && id > 0) return { view: "thread", id };
  }
  if (path.startsWith(sharePrefix)) {
    const token = decodeURIComponent(path.slice(sharePrefix.length));
    if (token !== "") return { view: "share", token };
  }
  if (path === "/repos") return { view: "repos" };
  if (path === "/shared") return { view: "shared" };
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
      return threadPrefix + route.id;
    case "repos":
      return "/repos";
    case "shared":
      return "/shared";
    case "share":
      return sharePrefix + encodeURIComponent(route.token);
    default:
      return "/new";
  }
}

/**
 * Pushes a route into history. Guarded on the current path so that landing on
 * a thread from its own URL does not push the same entry a second time — Back
 * would then need two presses to leave it.
 */
export function navigate(route: Route) {
  const path = pathForRoute(route);
  if (window.location.pathname !== path) window.history.pushState({}, "", path);
}
