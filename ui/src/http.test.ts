import { describe, it, expect } from "vitest";
import { serverMessage } from "./http";

describe("serverMessage", () => {
  it("reads the error field of a JSON body, the family's shape", async () => {
    const res = new Response(JSON.stringify({ error: "wrong team secret" }), {
      status: 401,
      headers: { "Content-Type": "application/json" },
    });
    expect(await serverMessage(res)).toBe("wrong team secret");
  });

  it("keeps a plain-text body as the message", async () => {
    const res = new Response("not in the checkout", { status: 404 });
    expect(await serverMessage(res)).toBe("not in the checkout");
  });

  it("names the status when the body says nothing", async () => {
    const res = new Response("", { status: 502 });
    expect(await serverMessage(res)).toBe("The server answered with 502.");
  });
});
