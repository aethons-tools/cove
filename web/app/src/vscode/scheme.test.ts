import { describe, expect, it } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { schemeOf, useVscodeScheme } from "./scheme";

const tokens = (...c: string[]) => {
  const el = document.createElement("div");
  el.classList.add(...c);
  return el.classList;
};

describe("schemeOf", () => {
  it.each([
    [["vscode-dark"], "dark"],
    [["vscode-high-contrast"], "dark"],
    [["vscode-high-contrast-light"], "light"],
    [["vscode-high-contrast", "vscode-high-contrast-light"], "light"], // VS Code adds both for HC light
    [["vscode-light"], "light"],
    [[], "light"],
  ])("%j → %s", (c, want) => {
    expect(schemeOf(tokens(...c))).toBe(want);
  });
});

describe("useVscodeScheme", () => {
  it("follows the body's class", async () => {
    document.body.className = "vscode-light";
    const r = renderHook(() => useVscodeScheme());
    expect(r.result.current).toBe("light");
    await act(async () => {
      document.body.className = "vscode-dark";
      await new Promise((ok) => setTimeout(ok, 0)); // MutationObserver callbacks are microtasks
    });
    expect(r.result.current).toBe("dark");
    r.unmount();
    document.body.className = "";
  });
});
