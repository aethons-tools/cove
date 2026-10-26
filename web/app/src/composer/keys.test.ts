import { describe, expect, it } from "vitest";
import { hint, isDraftKey, isPasteAsCodeKey, isSendKey } from "./keys";

const k = (o: Partial<{ key: string; code: string; metaKey: boolean; ctrlKey: boolean; shiftKey: boolean; altKey: boolean; isComposing: boolean }>) =>
  ({ key: "", code: "", metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, isComposing: false, ...o });

describe("keys", () => {
  it("sends on Cmd+Enter (Mac) / Ctrl+Enter (elsewhere) only", () => {
    expect(isSendKey(k({ key: "Enter", metaKey: true }), true)).toBe(true);
    expect(isSendKey(k({ key: "Enter", ctrlKey: true }), true)).toBe(false);
    expect(isSendKey(k({ key: "Enter", ctrlKey: true }), false)).toBe(true);
    expect(isSendKey(k({ key: "Enter", metaKey: true }), false)).toBe(false);
    expect(isSendKey(k({ key: "Enter" }), false)).toBe(false);
    expect(isSendKey(k({ key: "Enter", ctrlKey: true, shiftKey: true }), false)).toBe(false);
    expect(isSendKey(k({ key: "Enter", ctrlKey: true, isComposing: true }), false)).toBe(false);
  });
  it("pastes as code on Shift+Cmd+V / Shift+Ctrl+V", () => {
    expect(isPasteAsCodeKey(k({ code: "KeyV", shiftKey: true, metaKey: true }), true)).toBe(true);
    expect(isPasteAsCodeKey(k({ code: "KeyV", shiftKey: true, ctrlKey: true }), false)).toBe(true);
    expect(isPasteAsCodeKey(k({ code: "KeyV", ctrlKey: true }), false)).toBe(false);
    expect(isPasteAsCodeKey(k({ code: "KeyV", shiftKey: true, ctrlKey: true, altKey: true }), false)).toBe(false);
  });
  it("pushes/pops drafts on Cmd/Ctrl+↓/↑", () => {
    expect(isDraftKey(k({ key: "ArrowDown", metaKey: true }), true)).toBe("push");
    expect(isDraftKey(k({ key: "ArrowUp", ctrlKey: true }), false)).toBe("pop");
    expect(isDraftKey(k({ key: "ArrowUp", ctrlKey: true, shiftKey: true }), false)).toBeNull();
  });
  it("labels the keys for the platform", () => {
    expect(hint(true)).toBe("⌘↵ to send · ⇧⌘V pastes as code · ⌘↓ stacks a draft");
    expect(hint(false)).toBe("Ctrl+↵ to send · ⇧Ctrl+V pastes as code · Ctrl+↓ stacks a draft");
  });
});
