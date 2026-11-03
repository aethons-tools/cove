import { afterEach, describe, expect, it, vi } from "vitest";
import { copyText } from "./copy";

afterEach(() => {
  Reflect.deleteProperty(navigator, "clipboard");
  Reflect.deleteProperty(window, "isSecureContext");
  Reflect.deleteProperty(document, "execCommand");
});

function stub(writeText: () => Promise<void>, exec: (c: string) => boolean) {
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
  Object.defineProperty(document, "execCommand", { configurable: true, value: vi.fn(exec) });
}

describe("copyText", () => {
  it("uses the async clipboard when it can", async () => {
    stub(() => Promise.resolve(), () => false);
    expect(await copyText("a")).toBe(true);
    expect(document.execCommand).not.toHaveBeenCalled();
  });

  it("falls back to execCommand when the async clipboard refuses", async () => {
    stub(() => Promise.reject(new DOMException("denied", "NotAllowedError")), () => true);
    expect(await copyText("a")).toBe(true);
    expect(document.execCommand).toHaveBeenCalledWith("copy");
  });

  it("says false when neither copies", async () => {
    stub(() => Promise.reject(new Error("no")), () => false);
    expect(await copyText("a")).toBe(false);
  });
});
