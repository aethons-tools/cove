import { ActionIcon, NativeSelect, TextInput } from "@mantine/core";
import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import "./quietgrid.css";

export type GridColumn<R> = {
  key: keyof R & string;
  label: string;
  // choices makes the column a fixed set: a select while edited, its ▾ on hover.
  choices?: readonly string[];
  placeholder?: string;
  mono?: boolean;
  width?: number | string;
};

// GridErrors are per row (by index, like rows: recompute them when rows
// change), per column: the message under the cell.
export type GridErrors<R> = (Partial<Record<keyof R & string, string>> | undefined)[];

type At = { row: number; col: number };

// QuietGrid is a short editable list that looks like a plain table: a cell
// becomes a field only while edited (click it; Tab, Shift+Tab or Enter moves
// on; Esc leaves it). The "+ new …" row at the bottom adds an item (blank())
// until max; each row has a remove. Controlled: rows in, onChange out.
// readOnly shows the same table without editing.
export function QuietGrid<R extends Record<string, string>>({ columns, rows, onChange, blank, noun, max, errors, readOnly = false }: {
  columns: GridColumn<R>[]; rows: R[]; onChange: (rows: R[]) => void; blank: () => R;
  noun: string; max?: number; errors?: GridErrors<R>; readOnly?: boolean;
}) {
  const [editing, setEditing] = useState<At | null>(null);
  const uid = useId();
  const table = useRef<HTMLTableElement>(null);
  const refocus = useRef<string | null>(null);
  useEffect(() => {
    if (refocus.current) table.current?.querySelector<HTMLElement>(refocus.current)?.focus();
    refocus.current = null;
  });
  const cellSel = (at: At) => `[data-cell="${at.row}:${at.col}"]`;
  const leave = (focus: string | null) => { refocus.current = focus; setEditing(null); };
  const set = (row: number, key: keyof R & string, v: string) =>
    onChange(rows.map((r, i) => (i === row ? { ...r, [key]: v } : r)));
  const canAdd = !readOnly && (max === undefined || rows.length < max);
  const add = () => { setEditing({ row: rows.length, col: 0 }); onChange([...rows, blank()]); };
  // Focus stays on a remove button (the next row's, or the new last row's),
  // or the add row once the list is empty.
  const remove = (row: number) => {
    refocus.current = rows.length > 1 ? `[data-remove="${Math.min(row, rows.length - 2)}"]` : "[data-add]";
    setEditing(null);
    onChange(rows.filter((_, i) => i !== row));
  };

  const keys = (at: At) => (e: KeyboardEvent) => {
    if (e.nativeEvent.isComposing) return; // an IME's Enter confirms its text
    if (e.key === "Escape") {
      e.preventDefault();
      leave(cellSel(at));
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (at.row + 1 < rows.length) setEditing({ row: at.row + 1, col: at.col });
      else leave(cellSel(at));
    } else if (e.key === "Tab") {
      const n = at.row * columns.length + at.col + (e.shiftKey ? -1 : 1);
      if (n < 0) return; // out of the grid backwards: the browser moves focus, the blur leaves the cell
      e.preventDefault();
      if (n >= rows.length * columns.length) leave(canAdd ? "[data-add]" : cellSel(at));
      else setEditing({ row: Math.floor(n / columns.length), col: n % columns.length });
    }
  };

  return (
    <table className="jam-qgrid" ref={table}>
      <thead>
        <tr>
          {columns.map((c) => <th key={c.key} style={{ width: c.width }}>{c.label}</th>)}
          {!readOnly && <th style={{ width: 30 }} />}
        </tr>
      </thead>
      <tbody>
        {rows.map((r, row) => (
          <tr key={row}>
            {columns.map((c, col) => {
              const at = { row, col };
              const err = errors?.[row]?.[c.key];
              const label = `${c.label}, row ${row + 1}`;
              const errId = err ? `${uid}-err-${row}-${col}` : undefined;
              const on = editing?.row === row && editing.col === col;
              const field = {
                // Mantine shows (and describes the field by) the error while edited.
                "aria-label": label, autoFocus: true, size: "xs" as const, error: err,
                // Esc leaves the cell, not an enclosing Modal or Drawer.
                "data-mantine-stop-propagation": true,
                onKeyDown: keys(at),
                onBlur: () => setEditing((e) => (e?.row === row && e.col === col ? null : e)),
                styles: c.mono ? { input: { fontFamily: "var(--mantine-font-family-monospace)" } } : undefined,
              };
              return (
                <td key={c.key} data-error={err ? "true" : undefined}>
                  {on && c.choices ? (
                    <NativeSelect {...field} data={c.choices.includes(r[c.key]!) ? [...c.choices] : [{ value: "", label: "Choose…", disabled: true }, ...c.choices]} value={r[c.key]} onChange={(e) => set(row, c.key, e.currentTarget.value)} />
                  ) : on ? (
                    <TextInput {...field} placeholder={c.placeholder} value={r[c.key]} onChange={(e) => set(row, c.key, e.currentTarget.value)} />
                  ) : readOnly ? (
                    <div className={`cell${c.mono ? " mono" : ""}`}>{r[c.key]}</div>
                  ) : (
                    <button type="button" data-cell={`${row}:${col}`} aria-label={`${label}: ${r[c.key] || "empty"}`} aria-describedby={errId} className={`cell${c.mono ? " mono" : ""}${c.choices ? " choice" : ""}`} onClick={() => setEditing(at)}>
                      {r[c.key] ? <span>{r[c.key]}</span> : <span className="empty">{c.placeholder}</span>}
                      {c.choices && <span className="caret" aria-hidden="true">▾</span>}
                    </button>
                  )}
                  {err && !on && <div className="err" id={errId}>{err}</div>}
                </td>
              );
            })}
            {!readOnly && (
              <td>
                <ActionIcon variant="subtle" color="gray" mt={5} data-remove={row} aria-label={`Remove row ${row + 1}`} onClick={() => remove(row)}>✕</ActionIcon>
              </td>
            )}
          </tr>
        ))}
        {canAdd && (
          <tr>
            <td colSpan={columns.length + 1}>
              <button type="button" className="add" data-add onClick={add}>+ new {noun}…</button>
            </td>
          </tr>
        )}
      </tbody>
    </table>
  );
}
