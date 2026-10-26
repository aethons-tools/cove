import { describe, expect, it } from "vitest";
import type { Conversation, MessageRow } from "../api/types";
import { messagesOf } from "./useConversation";

const m = (seq: number) => ({ seq }) as MessageRow;
const page = (...seqs: number[]) => ({ messages: seqs.map(m) }) as Conversation;

describe("messagesOf", () => {
  it("puts older pages first and keeps each seq once", () => {
    // Page 0 is the newest; an overlap (a page fetched before new messages
    // shifted the window) must not show a message twice.
    expect(messagesOf([page(5, 6, 7), page(3, 4, 5)]).map((x) => x.seq)).toEqual([3, 4, 5, 6, 7]);
  });
});
