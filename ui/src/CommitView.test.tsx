import { describe, it, expect, vi, afterEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import CommitView from "./CommitView";
import { type SourceRef } from "./SourceView";
import { forgeLine } from "./turns";

const source: SourceRef = {
  marker: 1,
  repo: "rongo",
  branch: "master",
  path: "",
  start_line: 0,
  end_line: 0,
  sha: "7f2a492abcdef",
  kind: "commit",
  subject: "Test sources are labelled",
  committed_at: "2026-09-17T10:00:00Z",
};

function serve(status: number, body: unknown) {
  const fetchMock = vi.fn(async () => ({
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
    text: async () => (typeof body === "string" ? body : JSON.stringify(body)),
  }));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => vi.unstubAllGlobals());

describe("CommitView", () => {
  it("reads the commit, shows its message and files, and opens an indexed file whole", async () => {
    const fetchMock = serve(200, {
      repo: "rongo",
      branch: "master",
      sha: "7f2a492abcdef",
      committed_at: "2026-09-17T10:00:00Z",
      subject: "Test sources are labelled",
      body: "So the model can tell a fake from the client.\n\n- one\n- two",
      files: [
        { path: "backend/internal/ask/answer.go", added: 12, deleted: 3, indexed: true },
        { path: "docs/plans/x.html", added: 1, deleted: 0, indexed: false },
      ],
    });
    const opened: SourceRef[] = [];

    render(<CommitView source={source} onClose={() => {}} onOpenFile={(r) => opened.push(r)} />);

    await waitFor(() => expect(screen.getByText("2 files")).toBeTruthy());
    const url = String((fetchMock.mock.calls[0] as unknown[])[0]);
    expect(url).toContain("/api/commit?");
    expect(url).toContain("repo=rongo");
    expect(url).toContain("sha=7f2a492abcdef");
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("So the model can tell a fake from the client.");
    expect(dialog.textContent).toContain("7f2a492");
    expect(dialog.textContent).toContain("2026-09-17");
    expect(dialog.textContent).toContain("+12");
    // Only the indexed path is a button; the skipped one is text.
    const buttons = screen.getAllByRole("button").filter((b) => b.textContent?.includes(".go") || b.textContent?.includes(".html"));
    expect(buttons.map((b) => b.textContent)).toEqual(["backend/internal/ask/answer.go"]);

    await userEvent.click(buttons[0]);
    expect(opened).toEqual([
      { marker: 1, repo: "rongo", branch: "master", path: "backend/internal/ask/answer.go", start_line: 1, end_line: 0 },
    ]);
  });

  it("reads through the endpoint it is given and reports a refusal", async () => {
    serve(404, "This commit is not in Rongo's checkout.");
    render(<CommitView source={source} onClose={() => {}} endpoint="/api/shares/tok/commit" />);
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("not in Rongo's checkout"));
    // With no onOpenFile nothing in the body is a button but the close.
    expect(screen.getAllByRole("button").length).toBe(1);
  });

  it("keeps Tab inside, so it cannot reach the page behind the scrim", async () => {
    // Given a commit with a file to open, under a page that has a control of
    // its own — the citation chips of the answer are such controls
    serve(200, {
      repo: "rongo",
      branch: "master",
      sha: "7f2a492abcdef",
      subject: "s",
      body: "",
      files: [{ path: "a.go", added: 1, deleted: 0, indexed: true }],
    });
    render(
      <>
        <button type="button">behind the scrim</button>
        <CommitView source={source} onClose={() => {}} onOpenFile={() => {}} />
      </>,
    );
    const file = await screen.findByRole("button", { name: "a.go" });
    const close = screen.getByLabelText("Close");
    const dialog = screen.getByRole("dialog");

    // When Tab is pressed on the dialog's last control, and Shift+Tab on its first
    file.focus();
    fireEvent.keyDown(document, { key: "Tab" });
    const afterTab = document.activeElement;
    close.focus();
    fireEvent.keyDown(document, { key: "Tab", shiftKey: true });

    // Then the ring wraps: the dialog is modal
    expect(afterTab).toBe(close);
    expect(document.activeElement).toBe(file);
    expect(dialog.contains(document.activeElement)).toBe(true);
  });

  it("keeps its scrolling body in the Tab ring when it has no file to open", async () => {
    // A shared page: every file is plain text, so Close is the only button.
    // A long message must still scroll by keyboard, which needs the body to
    // be a stop of its own rather than something the trap wraps past.
    serve(200, { repo: "rongo", branch: "master", sha: "7f2a492abcdef", subject: "s", body: "long", files: [] });
    render(<CommitView source={source} onClose={() => {}} endpoint="/api/shares/tok/commit" />);
    await screen.findByText("long");
    const body = screen.getByRole("dialog").querySelector<HTMLElement>('[tabindex="0"]');
    expect(body).not.toBeNull();

    // From the body, the last stop, Tab wraps to Close.
    body?.focus();
    fireEvent.keyDown(document, { key: "Tab" });

    expect(document.activeElement).toBe(screen.getByLabelText("Close"));
  });

  it("closes on Escape", async () => {
    serve(200, { files: [] });
    const onClose = vi.fn();
    render(<CommitView source={source} onClose={onClose} />);
    await userEvent.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalled();
  });
});

describe("forgeLine", () => {
  it("writes a commit citation as sha, day and subject", () => {
    expect(forgeLine(source)).toBe("rongo · 7f2a492 2026-09-17 Test sources are labelled (master)");
  });
});
