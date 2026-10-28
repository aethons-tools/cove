import { Code } from "@mantine/core";
import type { DiffLine } from "../api/types";

const prefix: Record<string, string> = { "+": "+ ", "-": "- ", "…": "… " };
const color: Record<string, string | undefined> = { "+": "var(--mantine-color-green-text)", "-": "var(--mantine-color-red-text)", "…": "var(--mantine-color-dimmed)" };

// DiffView shows a server-made line diff, one line per row.
export function DiffView({ lines }: { lines: DiffLine[] }) {
  return (
    <Code block>
      {lines.map((l, i) => <div key={i} style={{ color: color[l.op] }}>{(prefix[l.op] ?? "  ") + l.text}</div>)}
    </Code>
  );
}
