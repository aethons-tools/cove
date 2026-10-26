import type { ThinkingEvent } from "../../api/types";

// tev is a Thinking event for tests: a text line of stream s1 by default.
export const tev = (seq: number, over: Partial<ThinkingEvent> = {}): ThinkingEvent => ({
  stream: "s1", seq, turn: 1, at: 1000 * seq, kind: "text", tool: "", toolUseId: "", text: `said ${seq}`, detail: "",
  detailTruncated: 0, isError: false, truncatedBytes: 0, costUsd: 0, durationMs: 0, claudeSession: "", ...over,
});
