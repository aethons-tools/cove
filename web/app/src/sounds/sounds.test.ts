import { beforeEach, describe, expect, it } from "vitest";
import { loadSounds, storeSounds } from "./sounds";

beforeEach(() => localStorage.clear());

describe("sound settings", () => {
  it("defaults, and reads the old Inbox's key", () => {
    expect(loadSounds()).toEqual({ messages: "pop", needs: "chime", volume: 0.6 });
    localStorage.setItem("jam-sounds", JSON.stringify({ messages: "off", needs: "bell", volume: 0.2 }));
    expect(loadSounds()).toEqual({ messages: "off", needs: "bell", volume: 0.2 });
  });
  it("rejects junk", () => {
    localStorage.setItem("jam-sounds", JSON.stringify({ messages: "kazoo", needs: 3, volume: 7 }));
    expect(loadSounds()).toEqual({ messages: "pop", needs: "chime", volume: 0.6 });
  });
  it("stores for the page", () => {
    storeSounds({ messages: "ping", needs: "off", volume: 1 });
    expect(JSON.parse(localStorage.getItem("jam-sounds")!)).toEqual({ messages: "ping", needs: "off", volume: 1 });
  });
});
