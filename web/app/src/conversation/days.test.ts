import { describe, expect, it } from "vitest";
import { dayLabel, sameDay } from "./days";

describe("days", () => {
  const now = new Date(2026, 9, 26, 15, 0); // local time
  it("labels today, yesterday and older dates", () => {
    expect(dayLabel(new Date(2026, 9, 26, 0, 5).toISOString(), now)).toBe("Today");
    expect(dayLabel(new Date(2026, 9, 25, 23, 59).toISOString(), now)).toBe("Yesterday");
    expect(dayLabel(new Date(2026, 9, 20, 12).toISOString(), now)).toBe(new Date(2026, 9, 20, 12).toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric", year: "numeric" }));
  });
  it("compares local days", () => {
    expect(sameDay(new Date(2026, 9, 26, 0, 1).toISOString(), new Date(2026, 9, 26, 23, 59).toISOString())).toBe(true);
    expect(sameDay(new Date(2026, 9, 26, 0, 1).toISOString(), new Date(2026, 9, 25, 23, 59).toISOString())).toBe(false);
  });
});
