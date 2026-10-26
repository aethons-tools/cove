import { beforeEach, describe, expect, it } from "vitest";
import { loadDrafts, loadReply, saveDrafts, saveReply } from "./drafts";

beforeEach(() => sessionStorage.clear());

describe("drafts", () => {
  it("keeps a reply per recipient, under the old Inbox's key", () => {
    saveReply("chn_a", "half typed");
    expect(loadReply("chn_a")).toBe("half typed");
    expect(loadReply("chn_b")).toBe("");
    expect(sessionStorage.getItem("me-reply:chn_a")).toBe("half typed");
    saveReply("chn_a", "");
    expect(sessionStorage.getItem("me-reply:chn_a")).toBeNull();
  });
  it("stacks drafts per recipient, under the old Inbox's key", () => {
    saveDrafts("chn_a", [{ text: "one", sel: 2 }]);
    expect(loadDrafts("chn_a")).toEqual([{ text: "one", sel: 2 }]);
    expect(JSON.parse(sessionStorage.getItem("me-drafts:chn_a")!)).toEqual([{ text: "one", sel: 2 }]);
    expect(loadDrafts("chn_b")).toEqual([]);
  });
  it("tolerates junk in storage", () => {
    sessionStorage.setItem("me-drafts:chn_a", "{not json");
    expect(loadDrafts("chn_a")).toEqual([]);
  });
});
