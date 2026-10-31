import { describe, expect, it } from "vitest";
import { normalizeJamUrl, settings } from "../src/config";

describe("normalizeJamUrl", () => {
  it.each([
    ["https://jam.example.com/", "https://jam.example.com"],
    ["https://jam.example.com/app/user/", "https://jam.example.com"],
    ["  https://Jam.Example.com:8443  ", "https://jam.example.com:8443"],
    ["http://localhost:8081", "http://localhost:8081"],
    ["http://127.0.0.1:8081", "http://127.0.0.1:8081"],
    ["http://[::1]:8081/", "http://[::1]:8081"],
  ])("accepts %s", (raw, want) => {
    expect(normalizeJamUrl(raw)).toBe(want);
  });

  it.each(["http://jam.example.com", "ftp://x", "jam.example.com", "", "https://user:pw@jam.example.com"])("refuses %j", (raw) => {
    expect(normalizeJamUrl(raw)).toBeInstanceOf(Error);
  });

  it("names the setting when empty", () => {
    expect((normalizeJamUrl("") as Error).message).toBe("Set jam.url to your Jam's address");
  });
});

describe("settings", () => {
  it("reads jam.url and jam.cliPath", () => {
    const vals: Record<string, unknown> = { url: "https://jam.example.com/", cliPath: "/usr/local/bin/jam", urls: ["https://b.example"] };
    expect(settings((k) => vals[k])).toEqual({ url: "https://jam.example.com", cliPath: "/usr/local/bin/jam", urls: ["https://b.example"] });
  });

  it("defaults to unset", () => {
    const s = settings(() => undefined);
    expect(s.url).toBeInstanceOf(Error);
    expect(s.cliPath).toBe("");
    expect(s.urls).toEqual([]);
  });
});
