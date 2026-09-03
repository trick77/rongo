import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import SourceView from "./SourceView";

const source = {
  marker: 2,
  repo: "peeq",
  branch: "master",
  path: "backend/internal/sched/sched.go",
  start_line: 3,
  end_line: 4,
  sha: "0123abcdef",
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

describe("SourceView", () => {
  it("asks for the file at the cited commit and marks the cited lines", async () => {
    const fetchMock = serve(200, {
      repo: "peeq",
      branch: "master",
      path: source.path,
      sha: "0123abcdef",
      content: "package sched\n\nfunc One() {}\nfunc Two() {}\nfunc Three() {}\n",
    });

    render(<SourceView source={source} onClose={() => {}} />);

    await screen.findByText("func One() {}");
    const url = String((fetchMock.mock.calls[0] as unknown[])[0]);
    expect(url).toContain("repo=peeq");
    expect(url).toContain("path=backend%2Finternal%2Fsched%2Fsched.go");
    expect(url).toContain("sha=0123abcdef");

    const hits = document.querySelectorAll("[data-hit]");
    expect(Array.from(hits).map((h) => h.getAttribute("data-line"))).toEqual(["3", "4"]);
    // The trailing newline is not a sixth, empty line.
    expect(document.querySelectorAll("[data-line]").length).toBe(5);
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("sched.go");
    expect(dialog.textContent).toContain("0123abc");
    expect(dialog.textContent).toContain("lines 3–4");
  });

  it("shows the server's reason when the file cannot be shown", async () => {
    serve(404, "This file is not in rongo's checkout at the cited commit.");

    render(<SourceView source={source} onClose={() => {}} />);

    await waitFor(() => {
      expect(screen.getByRole("alert").textContent).toContain("not in rongo's checkout");
    });
  });

  it("says so when the file has shrunk past the cited range", async () => {
    serve(200, { content: "one\ntwo\n", sha: "9999999", branch: "master" });

    render(<SourceView source={{ ...source, start_line: 40, end_line: 42, sha: "" }} onClose={() => {}} />);

    await waitFor(() => {
      expect(screen.getByRole("status").textContent).toContain("changed since the answer was written");
    });
    expect(document.querySelectorAll("[data-hit]").length).toBe(0);
  });

  it("keeps Tab inside the dialog", async () => {
    serve(200, { content: "x\n", sha: "0123abcdef", branch: "master" });
    const user = userEvent.setup();

    render(<SourceView source={source} onClose={() => {}} />);
    await screen.findByText("x");

    await user.tab();
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Close" }));
    await user.tab({ shift: true });
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Close" }));
  });

  it("closes on Escape and on the close button", async () => {
    serve(200, { content: "x\n", sha: "0123abcdef", branch: "master" });
    const user = userEvent.setup();
    const onClose = vi.fn();

    render(<SourceView source={source} onClose={onClose} />);

    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalledTimes(2);
  });
});
