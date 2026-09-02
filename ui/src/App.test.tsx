import { StrictMode } from "react";
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App from "./App";

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

/**
 * Replaces window.location so a navigation can be observed instead of
 * performed, and so the gate can be given a query string to read.
 */
function stubLocation(href: (url: string) => void, search: string) {
  vi.stubGlobal("location", {
    search,
    get href() {
      return "";
    },
    set href(v: string) {
      href(v);
    },
    reload: vi.fn(),
  });
}

/**
 * Renders the app past its session gate. /api/me is the first request App
 * makes, so every test that wants to see the UI has to get through it first.
 */
async function renderSignedIn() {
  vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, json: async () => [] })));
  render(<App />);
  await screen.findByRole("heading", { level: 1 });
}

describe("App", () => {
  it("renders the heading with text rongo", async () => {
    await renderSignedIn();
    const heading = screen.getByRole("heading", { level: 1 });
    expect(heading.textContent).toBe("rongo");
  });

  it("schickt einen abgelaufenen Login zum Provider statt die App zu zeigen", async () => {
    // Ohne diese Weiche scheitert stattdessen jede einzelne Kachel mit ihrem
    // eigenen 401, und der Nutzer sieht eine kaputte App statt einer Anmeldung.
    const href = vi.fn();
    stubLocation(href, "");
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: false, status: 401, json: async () => ({}) })));

    render(<App />);

    await waitFor(() => expect(href).toHaveBeenCalledWith("/api/auth/login"));
    expect(screen.queryByRole("heading", { level: 1 })).toBeNull();
  });

  it("zeigt die App trotzdem, wenn /api/me am Netz scheitert", async () => {
    // Ein Netzfehler ist keine abgelaufene Sitzung. Wer hier weiterleitet,
    // wirft den Nutzer bei jedem Schluckauf des Backends zum Provider.
    const href = vi.fn();
    stubLocation(href, "");
    vi.stubGlobal("fetch", vi.fn(async () => { throw new Error("offline"); }));

    render(<App />);

    await screen.findByRole("heading", { level: 1 });
    expect(href).not.toHaveBeenCalled();
  });

  it("bleibt nach einem gescheiterten Callback stehen statt in eine Schleife zu laufen", async () => {
    // Ohne diese Weiche: /api/me sagt 401, das UI geht auf /api/auth/login, der
    // Provider hat noch eine Sitzung und antwortet ohne Rueckfrage, der
    // Callback scheitert wieder — eine enge Schleife ohne jede Meldung. Zwei
    // gleichzeitig geoeffnete Tabs reichen aus, um sie auszuloesen.
    const href = vi.fn();
    stubLocation(href, "?auth_error=oidc_callback_failed");
    const fetchMock = vi.fn(async () => ({ ok: false, status: 401, json: async () => ({}) }));
    vi.stubGlobal("fetch", fetchMock);

    render(<App />);

    await screen.findByRole("link", { name: "Anmelden" });
    expect(href).not.toHaveBeenCalled();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("bleibt nach dem Abmelden stehen statt sich sofort neu anzumelden", async () => {
    // rongo widerruft nur die eigene Sitzung; die des Providers bleibt. Wer
    // hier weiterleitet, bekommt ohne Rueckfrage ein neues Token und ist wieder
    // angemeldet — der Abmelden-Knopf taete sichtbar nichts.
    const href = vi.fn();
    stubLocation(href, "?signed_out=1");
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: false, status: 401, json: async () => ({}) })));

    render(<App />);

    await screen.findByRole("link", { name: "Anmelden" });
    expect(href).not.toHaveBeenCalled();
  });

  it("zeigt bei 5xx auf /api/me nicht die angemeldete App", async () => {
    // Eine durchgestylte App, deren saemtliche Kacheln danach einzeln
    // scheitern, ist die schlechtere Auskunft als ein klarer Hinweis.
    const href = vi.fn();
    stubLocation(href, "");
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: false, status: 500, json: async () => ({}) })));

    render(<App />);

    await screen.findByText(/HTTP 500/);
    expect(screen.queryByRole("heading", { level: 1 })).toBeNull();
  });

  it("meldet ab und folgt der redirect_url der Antwort", async () => {
    const href = vi.fn();
    stubLocation(href, "");
    const fetchMock = vi.fn(async (url: string) => ({
      ok: true,
      status: 200,
      json: async () =>
        String(url) === "/api/auth/logout" ? { redirect_url: "/?signed_out=1" } : [],
    }));
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByRole("heading", { level: 1 });

    await user.click(screen.getByRole("button", { name: "Abmelden" }));

    await waitFor(() => expect(href).toHaveBeenCalledWith("/?signed_out=1"));
    expect(fetchMock).toHaveBeenCalledWith("/api/auth/logout", { method: "POST" });
  });

  it("behaelt den laufenden Thread beim Wechsel auf Repos", async () => {
    // Unmounting Ask would drop the answer on screen while the stream keeps
    // writing into a dead component. The stored record only catches up once the
    // turn is finished, so a stream interrupted this way is lost for good.
    await renderSignedIn();
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Frage"), "Eine Frage, die stehen bleiben muss");

    await user.click(screen.getByRole("button", { name: "Repos" }));
    await user.click(screen.getByRole("button", { name: "Fragen" }));

    expect((screen.getByLabelText("Frage") as HTMLTextAreaElement).value).toBe(
      "Eine Frage, die stehen bleiben muss",
    );
  });
});

/** Answers the thread list and one thread's turns separately. */
function apiFetch(threads: unknown, messages: unknown) {
  const mock = vi.fn(async (url: string) => ({
    ok: true,
    status: 200,
    json: async () => (String(url).startsWith("/api/threads/") ? messages : threads),
  }));
  vi.stubGlobal("fetch", mock);
  return mock;
}

const oneThread = [{ id: 7, title: "Wie laeuft der Versand?", created_at: "2026-08-17T10:00:00Z" }];
const oneTurn = [
  {
    id: 1,
    ordinal: 0,
    audience: "ba",
    question: "Wie laeuft der Versand?",
    answer: "Ueber einen Job [1].",
    error: "",
    citations: [],
    created_at: "2026-08-17T10:00:00Z",
  },
];

describe("App, der Thread ueber einen Neuladen hinweg", () => {
  it("holt den zuletzt offenen Thread zurueck", async () => {
    localStorage.setItem("rongo.thread", "7");
    apiFetch(oneThread, oneTurn);
    render(<StrictMode><App /></StrictMode>);
    expect(await screen.findByText(/Ueber einen Job/)).toBeTruthy();
  });

  it("merkt sich den gewaehlten Thread", async () => {
    apiFetch(oneThread, oneTurn);
    const user = userEvent.setup();
    render(<StrictMode><App /></StrictMode>);
    await user.click(await screen.findByRole("button", { name: "Wie laeuft der Versand?" }));
    await waitFor(() => expect(localStorage.getItem("rongo.thread")).toBe("7"));
  });

  // A thread that is not yours, or was purged, comes back as an empty list with
  // status 200. Keeping the id would make every later reload open nothing.
  it("vergisst eine Thread-Nummer, die ins Leere fuehrt", async () => {
    localStorage.setItem("rongo.thread", "999");
    apiFetch([], []);
    render(<StrictMode><App /></StrictMode>);
    await waitFor(() => expect(localStorage.getItem("rongo.thread")).toBeNull());
  });
});
