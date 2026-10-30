import { describe, expect, it } from "vitest";
import { route } from "../src/uri";

const id = "ses_" + "0123456789abcdefghjkmnpqrs";

describe("route", () => {
  it("/auth is auth", () => {
    const r = route("/auth", "code=c&state=s");
    expect(r.kind).toBe("auth");
    if (r.kind === "auth") expect(r.query.get("code")).toBe("c");
  });
  it("/session/<id> is a session", () => {
    expect(route(`/session/${id}`, "")).toEqual({ kind: "session", id });
  });
  it("/hub is the hub", () => {
    expect(route("/hub", "")).toEqual({ kind: "hub" });
    expect(route("/hub/x", "").kind).toBe("bad");
  });
  it("anything else is bad", () => {
    expect(route("/nope", "").kind).toBe("bad");
    expect(route("/session/ses_x", "").kind).toBe("bad");
    expect(route(`/session/${id}/x`, "").kind).toBe("bad");
  });
});
