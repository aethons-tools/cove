import { Combobox, TextInput, Textarea, useCombobox } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";

// suggest reads the admin's type-ahead list (GET /app/admin/suggest).
async function suggest(kind: string, project: string): Promise<string[]> {
  const q = new URLSearchParams({ kind, project });
  const res = await fetch(`/app/admin/suggest?${q}`, { credentials: "same-origin", headers: { Accept: "application/json" } });
  if (!res.ok) return [];
  return (await res.json()) as string[];
}

// token is the part of value being typed: with list, after the current
// line's last comma; else the current line (multiline) or the whole value.
function token(value: string, list: boolean): { head: string; tail: string } {
  const lineStart = value.lastIndexOf("\n") + 1;
  const cut = list ? Math.max(lineStart, value.lastIndexOf(",") + 1) : lineStart;
  return { head: value.slice(0, cut), tail: value.slice(cut) };
}

// TypeAhead is a text field (or, multiline, a textarea) that suggests names
// of kind (users, kits, targets…) for the token being typed; with list, each
// comma-separated token.
export function TypeAhead({ label, description, kind, project = "", list = false, multiline = false, required, value, onChange, placeholder }: {
  label: string; description?: ReactNode; kind: string; project?: string; list?: boolean; multiline?: boolean;
  required?: boolean; value: string; onChange: (v: string) => void; placeholder?: string;
}) {
  const combobox = useCombobox();
  const { data = [] } = useQuery({ queryKey: ["admin", "suggest", kind, project], queryFn: () => suggest(kind, project), staleTime: 30_000 });
  const { head, tail } = token(value, list);
  const needle = tail.trim().toLowerCase();
  const options = data.filter((s) => s.toLowerCase().includes(needle) && s !== tail.trim()).slice(0, 12);
  const field = {
    label, description, required, placeholder, value,
    autoComplete: "off", spellCheck: false,
    onChange: (e: { currentTarget: { value: string } }) => { onChange(e.currentTarget.value); combobox.openDropdown(); },
    onFocus: () => combobox.openDropdown(),
    onBlur: () => combobox.closeDropdown(),
  };
  return (
    <Combobox store={combobox} onOptionSubmit={(v) => { onChange(head + v); combobox.closeDropdown(); }}>
      <Combobox.Target>
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
