import { describe, expect, it } from "vitest";
import { asCodeBlock, fenceFor } from "./fence";

describe("fence", () => {
  it("is longer than any backtick run, at least three", () => {
    expect(fenceFor("plain")).toBe("```");
    expect(fenceFor("a ```b``` c")).toBe("````");
    expect(fenceFor("x `````")).toBe("``````");
  });
  it("wraps pasted text on its own lines", () => {
    expect(asCodeBlock("a ```b``` c", "see:")).toBe("\n````\na ```b``` c\n````\n");
    expect(asCodeBlock("x\n", "")).toBe("```\nx\n```\n");
    expect(asCodeBlock("x", "line\n")).toBe("```\nx\n```\n");
  });
});
