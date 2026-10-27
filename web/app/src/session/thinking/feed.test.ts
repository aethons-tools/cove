import { describe, expect, it } from "vitest";
import { emptyFeed, feedReducer, type Feed, type FeedAction } from "./feed";
import { fmtDuration, usd } from "./format";
import { tev } from "./testing";

const run = (max: number, ...as: FeedAction[]): Feed => as.reduce(feedReducer(max), emptyFeed);
const seqs = (f: Feed) => f.items.map((i) => (i.t === "ev" ? i.ev.seq : i.t));
const live = (seq: number, following = true): FeedAction => ({ type: "live", ev: tev(seq), following });

describe("the Thinking feed", () => {
  it("appends live events once each, counting the unseen when not following", () => {
    const f = run(10, { type: "stream", stream: "s1" }, live(1), live(2), live(2), live(3, false));
    expect(seqs(f)).toEqual([1, 2, 3]);
    expect(f.stream).toBe("s1");
    expect(f.unseen).toBe(1);
    expect(run(10, live(1, false), { type: "seen" }).unseen).toBe(0);
  });

  it("caps live events at max, dropping the oldest and offering Load earlier", () => {
    const f = run(3, live(1), live(2), live(3), live(4), live(5));
    expect(seqs(f)).toEqual([3, 4, 5]);
    expect(f.more).toBe(3);
  });

  it("inserts a Load earlier page above, then trims the newest past max and pauses", () => {
    let f = run(4, live(5), live(6), { type: "more", before: 5 });
    f = feedReducer(4)(f, { type: "page", events: [tev(3), tev(4)], at: null, from: 0, before: 3 });
    expect(seqs(f)).toEqual([3, 4, 5, 6]);
    expect(f.more).toBe(3);
    expect(f.paused).toBe(false);
    f = feedReducer(4)(f, { type: "page", events: [tev(1), tev(2)], at: null, from: 0, before: 0 });
    expect(seqs(f)).toEqual([1, 2, 3, 4]); // 5 and 6 trimmed
    expect(f.more).toBe(0);
    expect(f.paused).toBe(true);
    expect(f.unseen).toBe(2);
    // Paused: live events only count; the loaded pages stay.
    f = feedReducer(4)(f, live(7));
    expect(seqs(f)).toEqual([1, 2, 3, 4]);
    expect(f.unseen).toBe(3);
    expect(feedReducer(4)(f, { type: "seen" }).unseen).toBe(3); // seen doesn't clear a pause's count
  });

  it("marks a reconnect's hole, and fills it from below: shrinking, then gone", () => {
    let f = run(100, live(1), live(2), { type: "skipped", from: 3, to: 9 }, live(10));
    expect(seqs(f)).toEqual([1, 2, "skip", 10]);
    const key = f.items[2]!.t === "skip" ? f.items[2]!.key : "";
    f = feedReducer(100)(f, { type: "page", events: [tev(6), tev(7), tev(8), tev(9)], at: key, from: 3, before: 6 });
    expect(seqs(f)).toEqual([1, 2, "skip", 6, 7, 8, 9, 10]);
    expect(f.items[2]).toMatchObject({ t: "skip", from: 3, to: 5 });
    f = feedReducer(100)(f, { type: "page", events: [tev(2), tev(3), tev(4), tev(5)], at: key, from: 3, before: 2 });
    expect(seqs(f)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]); // 2 deduped; the marker gone
  });

  it("adds a restart divider in place, and keeps the lines above", () => {
    const f = run(100, live(1), { type: "restart" }, { type: "stream", stream: "s2" }, { type: "live", ev: tev(1, { stream: "s2" }), following: true });
    expect(seqs(f)).toEqual([1, "restart", 1]);
    expect(f.stream).toBe("s2");
  });

  it("resets, and carries a note", () => {
    expect(run(10, live(1), { type: "reset" })).toEqual(emptyFeed);
    expect(run(10, { type: "note", text: "gone" }).note).toBe("gone");
  });
});

describe("Thinking formats", () => {
  it("renders durations and dollars as the old Inbox did", () => {
    expect([fmtDuration(850), fmtDuration(2100), fmtDuration(2000), fmtDuration(64000), fmtDuration(-5)]).toEqual(["850ms", "2.1s", "2s", "1m 4s", "0ms"]);
    expect([usd(0.12), usd(1234.5)]).toEqual(["$0.12", "$1,234.50"]);
  });
});
