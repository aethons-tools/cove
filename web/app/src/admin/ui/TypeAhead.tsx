import { Combobox, TextInput, Textarea, useCombobox } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useRef, useState, type ReactNode } from "react";

// suggest reads the admin's type-ahead list (GET /app/admin/suggest).
async function suggest(kind: string, project: string): Promise<string[]> {
  const q = new URLSearchParams({ kind, project });
  const res = await fetch(`/app/admin/suggest?${q}`, { credentials: "same-origin", headers: { Accept: "application/json" } });
  if (!res.ok) return [];
  return (await res.json()) as string[];
}

// tokenAt is the token being typed at caret: from the line start (or, with
// list, the last comma before the caret) to the next comma, "@" or line end.
// null inside a timeout (after "@"): there is nothing to complete there.
export function tokenAt(value: string, caret: number, list: boolean): { start: number; end: number; text: string } | null {
  const lineStart = value.lastIndexOf("\n", caret - 1) + 1;
  const start = list ? Math.max(lineStart, value.lastIndexOf(",", caret - 1) + 1) : lineStart;
  if (value.slice(start, caret).includes("@")) return null;
  let end = caret;
  while (end < value.length && !"@\n".includes(value[end]!) && !(list && value[end] === ",")) end++;
  return { start, end, text: value.slice(start, end) };
}

// TypeAhead is a text field (or, multiline, a textarea) that suggests names
// of kind (users, kits, targets…) for the token at the caret; with list, each
// comma-separated token.
export function TypeAhead({ label, description, kind, project = "", list = false, multiline = false, required, value, onChange, placeholder }: {
  label: string; description?: ReactNode; kind: string; project?: string; list?: boolean; multiline?: boolean;
  required?: boolean; value: string; onChange: (v: string) => void; placeholder?: string;
}) {
  const combobox = useCombobox();
  const ref = useRef<HTMLInputElement & HTMLTextAreaElement>(null);
  const [caret, setCaret] = useState(value.length);
  const { data = [] } = useQuery({ queryKey: ["admin", "suggest", kind, project], queryFn: () => suggest(kind, project), staleTime: 30_000 });
  const tok = tokenAt(value, Math.min(caret, value.length), list);
  const needle = tok?.text.trim().toLowerCase() ?? "";
  const options = tok ? data.filter((s) => s.toLowerCase().includes(needle) && s.toLowerCase() !== needle).slice(0, 12) : [];
  const track = () => setCaret(ref.current?.selectionStart ?? value.length);
  const field = {
    ref, label, description, required, placeholder, value,
    autoComplete: "off", spellCheck: false,
    onChange: (e: { currentTarget: { value: string; selectionStart: number | null } }) => {
      onChange(e.currentTarget.value);
      setCaret(e.currentTarget.selectionStart ?? e.currentTarget.value.length);
      combobox.openDropdown();
    },
    onClick: track,
    onKeyUp: track,
    onBlur: () => combobox.closeDropdown(),
  };
  const pick = (v: string) => {
    if (!tok) return;
    const lead = tok.text.match(/^\s*/)?.[0] ?? "";
    const next = value.slice(0, tok.start) + lead + v + value.slice(tok.end);
    const at = tok.start + lead.length + v.length;
    onChange(next);
    setCaret(at);
    combobox.closeDropdown();
    requestAnimationFrame(() => ref.current?.setSelectionRange(at, at));
  };
  return (
    <Combobox store={combobox} onOptionSubmit={pick}>
      <Combobox.Target withKeyboardNavigation={options.length > 0}>
        {multiline ? <Textarea {...field} autosize minRows={3} /> : <TextInput {...field} />}
      </Combobox.Target>
      <Combobox.Dropdown hidden={options.length === 0}>
        <Combobox.Options>
          {options.map((o) => <Combobox.Option key={o} value={o}>{o}</Combobox.Option>)}
        </Combobox.Options>
      </Combobox.Dropdown>
    </Combobox>
  );
}
