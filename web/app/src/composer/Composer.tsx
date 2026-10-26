import { Button, Checkbox, Group, Paper, Textarea } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { apiPost, ApiError } from "../api/client";
import type { MessageSent, SendMessageRequest } from "../api/types";
import { asCodeBlock } from "./fence";
import { loadDrafts, loadReply, saveDrafts, saveReply } from "./drafts";
import * as keys from "./keys";

// Composer is the reply box for `to` (a channel id, or user:/session: for a
// new message). Text is kept per recipient (drafts.ts); a failed send keeps
// everything and says why.
export function Composer({ to, label, attachMax, onSent }: { to: string; label: string; attachMax: number; onSent?: (s: MessageSent) => void }) {
  void attachMax; // attachments: Task 4
  const mac = keys.isMac();
  const client = useQueryClient();
  const box = useRef<HTMLTextAreaElement>(null);
  const asCode = useRef(false);
  const [text, setTextState] = useState(() => loadReply(to));
  const [plain, setPlain] = useState(false);
  const [drafts, setDraftsState] = useState(() => loadDrafts(to));
  const [sending, setSending] = useState(false);

  // State is read once per recipient: callers key the composer by `to`.
  const setText = (t: string) => { setTextState(t); saveReply(to, t); };
  const setDrafts = (d: typeof drafts) => { setDraftsState(d); saveDrafts(to, d); };

  const push = () => {
    if (!text.trim()) return false;
    setDrafts([...drafts, { text, sel: box.current?.selectionStart ?? text.length }]);
    setText("");
    return true;
  };
  const pop = (current = text) => {
    if (current.trim() || drafts.length === 0) return false;
    const top = drafts[drafts.length - 1]!;
    setDrafts(drafts.slice(0, -1));
    setText(top.text);
    requestAnimationFrame(() => { const s = Math.min(top.sel, top.text.length); box.current?.setSelectionRange(s, s); box.current?.focus(); });
    return true;
  };

  async function send() {
    const body = text.trim();
    if (!body || sending) return;
    setSending(true);
    const req: SendMessageRequest = { to, body, ...(plain ? { contentType: "text/plain" } : {}) };
    try {
      const sent = await apiPost<MessageSent>("messages", req);
      setText("");
      setPlain(false);
      pop("");
      void client.invalidateQueries({ queryKey: ["conversation", sent.channel] });
      void client.invalidateQueries({ queryKey: ["rail"] });
      onSent?.(sent);
    } catch (e) {
      notifications.show({ color: "red", message: e instanceof ApiError ? e.message : String(e) });
    } finally {
      setSending(false);
    }
  }

  return (
    <Paper p="xs" withBorder radius={0} style={{ borderLeft: 0, borderRight: 0, borderBottom: 0 }}>
      {drafts.length > 0 && (
        <Button size="compact-xs" variant="light" mb={4} title={`Restore the top stacked draft (${mac ? "⌘↑" : "Ctrl+↑"}) — the box must be empty`} onClick={() => pop()}>
          ↩ {drafts.length} stacked draft{drafts.length === 1 ? "" : "s"}
        </Button>
      )}
      <Group align="flex-end" gap="xs" wrap="nowrap">
        <Textarea ref={box} flex={1} autosize minRows={2} maxRows={12} aria-label={`Message ${label}`}
          placeholder={`Message ${label}… (${keys.hint(mac)})`} value={text}
          onChange={(e) => setText(e.currentTarget.value)}
          onKeyDown={(e) => {
            asCode.current = keys.isPasteAsCodeKey(e, mac);
            // The native event carries isComposing (React's synthetic one doesn't).
            if (keys.isSendKey(e.nativeEvent, mac)) { e.preventDefault(); void send(); return; }
            const d = keys.isDraftKey(e, mac);
            if (d === "push" && push()) e.preventDefault();
            else if (d === "pop" && pop()) e.preventDefault();
          }}
          onPaste={(e) => {
            if (!asCode.current) return;
            asCode.current = false;
            const pasted = e.clipboardData?.getData("text/plain") ?? "";
            if (!pasted) return;
            e.preventDefault();
            const t = e.currentTarget, start = t.selectionStart, end = t.selectionEnd;
            const block = asCodeBlock(pasted, t.value.slice(0, start));
            // execCommand keeps the browser's undo; jsdom lacks it.
            if (!document.execCommand?.("insertText", false, block)) setText(t.value.slice(0, start) + block + t.value.slice(end));
          }} />
        <Group gap={6} wrap="nowrap">
          <Checkbox size="xs" label="Plain text" title="Send as plain text: shown literally, not rendered as markdown" checked={plain} onChange={(e) => setPlain(e.currentTarget.checked)} />
          <Button size="sm" loading={sending} onClick={() => void send()}>Send {mac ? "⌘↵" : "Ctrl+↵"}</Button>
        </Group>
      </Group>
    </Paper>
  );
}
