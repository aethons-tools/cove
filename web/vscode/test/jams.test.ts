import { describe, expect, it } from "vitest";
import { appFor, hubFileFor, hubJamOf, jamList, jamSlug, pendingFileFor } from "../src/jams";

const A = "https://a.example";
const B = "https://b.example";

describe("jams", () => {
  it("jamList is jam.url then jam.urls, normalised, de-duplicated, valid only", () => {
    expect(jamList("https://a.example/", ["https://b.example/", A, "nope", 3])).toEqual({ list: [A, B], bad: ["nope", "3"] });
    expect(jamList("", [B])).toEqual({ list: [B], bad: [] });
    expect(jamList(A, undefined)).toEqual({ list: [A], bad: [] });
    expect(jamList(A, "not an array")).toEqual({ list: [A], bad: [] });
  });
  it("names per Jam", () => {
    expect(jamSlug("https://h:8443")).toBe("h-8443");
    expect(jamSlug("https://jam.example.com")).toBe("jam.example.com");
    expect(appFor(B, A)).toBe("vscode-b.example");
    expect(appFor(A, A)).toBe("default");
    expect(appFor(B, undefined)).toBe("vscode-b.example");
    expect(hubFileFor("https://jam.example.com")).toBe("Jam Hub (jam.example.com).code-workspace");
    expect(pendingFileFor("https://h:8443")).toBe("pending-h-8443.json");
  });
  it("hubJamOf maps a hub file back to its listed Jam", () => {
    expect(hubJamOf("Jam Hub (b.example).code-workspace", [A, B])).toBe(B);
    expect(hubJamOf("Jam Hub (c.example).code-workspace", [A, B])).toBeUndefined();
    expect(hubJamOf("Other.code-workspace", [A, B])).toBeUndefined();
  });
});
