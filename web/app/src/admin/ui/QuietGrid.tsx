import { ActionIcon, NativeSelect, TextInput } from "@mantine/core";
import { Fragment, useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import "./quietgrid.css";

// TextKey is a key of R whose value is a string: what a column edits.
export type TextKey<R> = { [K in keyof R]: R[K] extends string ? K : never }[keyof R] & string;

export type GridColumn<R> = {
  key: TextKey<R>;
  label: string;
  // choices makes the column a fixed set: a select while edited, its ▾ on hover.
  choices?: readonly string[];
  placeholder?: string;
  mono?: boolean;
  width?: number | string;
  // show is the cell's text while not edited (default: its value, or the placeholder when empty).
  show?: (r: R) => ReactNode;
};

// GridExpand makes each row collapsible: ▸ opens a full-width detail under
// it, ▾ closes it. open and toggle read and flip the row's own open state.
export type GridExpand<R> = {
  open: (r: R) => boolean;
  toggle: (r: R) => R;
  detail: (r: R, row: number) => ReactNode;
};

// GridErrors are per row (by index, like rows: recompute them when rows
// change), per column: the message under the cell.
export type GridErrors<R> = (Partial<Record<TextKey<R>, string>> | undefined)[];

type At = { row: number; col: number };

// QuietGrid is a short editable list that looks like a plain table: a cell
// becomes a field only while edited (click it; Tab, Shift+Tab or Enter moves
// on; Esc leaves it). The "+ new …" row at the bottom adds an item (blank())
// until max; each row has a remove. Controlled: rows in, onChange out.
// readOnly shows the same table without editing; expand (GridExpand) works
// in both.
export function QuietGrid<R extends object>({ columns, rows, onChange, blank, noun, max, errors, readOnly = false, expand }: {
  columns: GridColumn<R>[]; rows: R[]; onChange: (rows: R[]) => void; blank: () => R;
  noun: string; max?: number; errors?: GridErrors<R>; readOnly?: boolean; expand?: GridExpand<R>;
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
  const val = (r: R, key: TextKey<R>) => r[key] as string;
  const set = (row: number, key: TextKey<R>, v: string) =>
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
          {expand && <th style={{ width: 28 }} />}
          {columns.map((c) => <th key={c.key} style={{ width: c.width }}>{c.label}</th>)}
          {!readOnly && <th style={{ width: 30 }} />}
        </tr>
      </thead>
      <tbody>
        {rows.map((r, row) => (
          <Fragment key={row}>
          <tr className={expand?.open(r) ? "open" : undefined}>
            {expand && (
              <td className="tog">
                <button type="button" data-toggle={row} aria-expanded={expand.open(r)} aria-label={`Details, row ${row + 1}`}
                  onClick={() => onChange(rows.map((x, i) => (i === row ? expand.toggle(x) : x)))}>{expand.open(r) ? "▾" : "▸"}</button>
              </td>
            )}
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
                    <NativeSelect {...field} data={c.choices.includes(val(r, c.key)) ? [...c.choices] : [{ value: "", label: "Choose…", disabled: true }, ...c.choices]} value={val(r, c.key)} onChange={(e) => set(row, c.key, e.currentTarget.value)} />
                  ) : on ? (
                    <TextInput {...field} placeholder={c.placeholder} value={val(r, c.key)} onChange={(e) => set(row, c.key, e.currentTarget.value)} />
                  ) : readOnly ? (
                    <div className={`cell${c.mono ? " mono" : ""}`}>{c.show ? c.show(r) : val(r, c.key)}</div>
                  ) : (
                    <button type="button" data-cell={`${row}:${col}`} aria-label={`${label}: ${val(r, c.key) || "empty"}`} aria-describedby={errId} className={`cell${c.mono ? " mono" : ""}${c.choices ? " choice" : ""}`} onClick={() => setEditing(at)}>
                      {c.show ? <span>{c.show(r)}</span> : val(r, c.key) ? <span>{val(r, c.key)}</span> : <span className="empty">{c.placeholder}</span>}
                      {c.choices && <span className="caret" aria-hidden="true">▾</span>}
                    </button>
                  )}
                  {err && !on && <div className="err" id={errId}>{err}</div>}
                </td>
              );
            })}
            {!readOnly && (
              <td>
                <ActionIcon variant="subtle" color="gray" mt={5} data-remove={row} aria-label={`Remove ${noun} ${row + 1}`} onClick={() => remove(row)}>✕</ActionIcon>
              </td>
            )}
          </tr>
          {expand?.open(r) && (
            <tr className="detail">
              <td colSpan={columns.length + (readOnly ? 1 : 2)}>{expand.detail(r, row)}</td>
            </tr>
          )}
          </Fragment>
        ))}
        {canAdd && (
          <tr>
            {expand && <td />}
            <td colSpan={columns.length + 1}>
              <button type="button" className="add" data-add onClick={add}>+ new {noun}…</button>
            </td>
          </tr>
        )}
      </tbody>
    </table>
  );
}
