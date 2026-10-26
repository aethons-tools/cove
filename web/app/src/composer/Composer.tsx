import { ActionIcon, Button, Checkbox, CloseButton, Group, Paper, Text, Textarea } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useQueryClient } from "@tanstack/react-query";
import { useImperativeHandle, useRef, useState, type Ref } from "react";
import { apiPost, ApiError } from "../api/client";
import type { MessageSent, SendMessageRequest } from "../api/types";
import { asCodeBlock } from "./fence";
import { loadDrafts, loadReply, saveDrafts, saveReply } from "./drafts";
import * as keys from "./keys";
import { AttachmentChips, type Pending } from "./AttachmentChips";
import { upload } from "./upload";

// Composer is the reply box for `to` (a channel id, or user:/session: for a
// new message). Text is kept per recipient (drafts.ts); a failed send keeps
// everything and says why.
// ComposerHandle lets the conversation start a reply about an ask (Discuss).
export type ComposerHandle = { discuss(id: string, question: string): void };

export function Composer({ to, label, attachMax, onSent, ref }: { to: string; label: string; attachMax: number; onSent?: (s: MessageSent) => void; ref?: Ref<ComposerHandle> }) {
  const mac = keys.isMac();
  const client = useQueryClient();
  const box = useRef<HTMLTextAreaElement>(null);
  const asCode = useRef(false);
  const [text, setTextState] = useState(() => loadReply(to));
  const [plain, setPlain] = useState(false);
  const [drafts, setDraftsState] = useState(() => loadDrafts(to));
  const [sending, setSending] = useState(false);
  const [replyTo, setReplyTo] = useState<{ id: string; question: string } | null>(null);
  const [files, setFiles] = useState<Pending[]>([]);
  const fileInput = useRef<HTMLInputElement>(null);
  const nextId = useRef(1);
  const uploading = files.some((f) => !f.ref && !f.error);

  // attach starts an upload per file; each chip tracks its progress, ref or
  // error; an aborted one (its ✕) disappears.
  function attach(list: Iterable<File>) {
    for (const file of list) {
      const id = nextId.current++;
      const patch = (p: Partial<Pending>) => setFiles((fs) => fs.map((f) => (f.id === id ? { ...f, ...p } : f)));
      const up = upload(file, (pct) => patch({ pct }));
      setFiles((fs) => [...fs, { id, name: file.name, pct: 0, abort: up.abort }]);
      up.done.then(
        (ref) => patch({ ref, pct: 100 }),
        (e: unknown) => {
          if (e instanceof DOMException && e.name === "AbortError") setFiles((fs) => fs.filter((f) => f.id !== id));
          else patch({ error: e instanceof Error ? e.message : String(e) });
        },
      );
    }
  }
  const remove = (id: number) => {
    const f = files.find((x) => x.id === id);
    if (f && !f.ref && !f.error) f.abort(); // the abort's rejection removes the chip
    else setFiles((fs) => fs.filter((x) => x.id !== id));
  };

  // State is read once per recipient: callers key the composer by `to`.
  const setText = (t: string) => { setTextState(t); saveReply(to, t); };
  // draftsRef is the stack now, for a send that lands after more pushes.
  const draftsRef = useRef(drafts);
  const setDrafts = (d: typeof drafts) => { draftsRef.current = d; setDraftsState(d); saveDrafts(to, d); };

  // discuss starts a reply about an ask: it carries replyTo, and an empty
  // box gets a "re:" line (as the old Inbox's meDiscuss); text already
  // there is never overwritten.
  useImperativeHandle(ref, () => ({
    discuss(id: string, question: string) {
      setReplyTo({ id, question });
      if (!box.current?.value.trim()) setText(`re: ${question}\n`);
      requestAnimationFrame(() => {
        const b = box.current;
        if (b) { b.focus(); b.setSelectionRange(b.value.length, b.value.length); }
      });
    },
  }));

  const push = () => {
    if (!text.trim()) return false;
    setDrafts([...draftsRef.current, { text, sel: box.current?.selectionStart ?? text.length }]);
    setText("");
    return true;
  };
  const pop = (current = text) => {
    const stack = draftsRef.current;
    if (current.trim() || stack.length === 0) return false;
    const top = stack[stack.length - 1]!;
    setDrafts(stack.slice(0, -1));
    setText(top.text);
    requestAnimationFrame(() => { const s = Math.min(top.sel, top.text.length); box.current?.setSelectionRange(s, s); box.current?.focus(); });
    return true;
  };

  async function send() {
    const body = text.trim();
    if (uploading) { notifications.show({ message: "Still uploading…" }); return; }
    const refs = files.flatMap((f) => (f.ref ? [f.ref] : []));
    if ((!body && refs.length === 0) || sending) return;
    setSending(true);
    const req: SendMessageRequest = { to, body, ...(plain ? { contentType: "text/plain" } : {}), ...(refs.length ? { attachments: refs } : {}), ...(replyTo ? { replyTo: replyTo.id } : {}) };
    try {
      const sent = await apiPost<MessageSent>("messages", req);
      setText("");
      setPlain(false);
      setFiles([]);
      setReplyTo(null);
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
    <Paper p="xs" withBorder radius={0} style={{ borderLeft: 0, borderRight: 0, borderBottom: 0 }}
      onDragOver={(e) => { if (attachMax > 0) e.preventDefault(); }}
      onDrop={(e) => {
        if (attachMax > 0 && e.dataTransfer.files.length) { e.preventDefault(); attach(e.dataTransfer.files); }
      }}>
      <AttachmentChips items={files} onRemove={remove} />
      {replyTo && (
        <Group gap={4} mb={4}>
          <Text size="xs" c="dimmed">Replying about: {replyTo.question}</Text>
          <CloseButton size="xs" aria-label={`Stop replying about ${replyTo.question}`} onClick={() => setReplyTo(null)} />
        </Group>
      )}
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
            // A pasted file (a screenshot) attaches — unless text came with
            // it (an Office copy carries a picture too) or it's paste-as-code.
            const cd = e.clipboardData;
            if (!asCode.current && attachMax > 0 && cd?.files?.length && !cd.getData("text/plain")) {
              e.preventDefault();
              attach(cd.files);
              return;
            }
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
          {attachMax > 0 && <>
            <input ref={fileInput} type="file" multiple hidden aria-label={`Attach files (or paste / drop them; max ${Math.round(attachMax / (1 << 20))} MB each)`}
              onChange={(e) => { if (e.currentTarget.files) attach(e.currentTarget.files); e.currentTarget.value = ""; }} />
            <ActionIcon variant="subtle" aria-label="Add attachments" title="Attach files (or paste / drop them)" onClick={() => fileInput.current?.click()}>📎</ActionIcon>
          </>}
          <Checkbox size="xs" label="Plain text" title="Send as plain text: shown literally, not rendered as markdown" checked={plain} onChange={(e) => setPlain(e.currentTarget.checked)} />
          <Button size="sm" loading={sending} disabled={uploading} onClick={() => void send()}>Send {mac ? "⌘↵" : "Ctrl+↵"}</Button>
        </Group>
      </Group>
    </Paper>
  );
}
