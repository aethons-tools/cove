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
  it("/go/<chn> is go, with an optional valid session", () => {
    const chn = "chn_" + "0123456789abcdefghjkmnpqrs";
    expect(route(`/go/${chn}`, "")).toEqual({ kind: "go", channel: chn, session: "" });
    expect(route(`/go/${chn}`, `session=${id}`)).toEqual({ kind: "go", channel: chn, session: id });
    expect(route("/go/chn_x", "").kind).toBe("bad");
    expect(route(`/go/${chn}`, "session=ses_x").kind).toBe("bad");
    expect(route(`/go/${chn}/x`, "").kind).toBe("bad");
    expect(route(`/go/${id}`, "").kind).toBe("bad");
  });
  it("anything else is bad", () => {
    expect(route("/nope", "").kind).toBe("bad");
    expect(route("/session/ses_x", "").kind).toBe("bad");
    expect(route(`/session/${id}/x`, "").kind).toBe("bad");
  });
});
