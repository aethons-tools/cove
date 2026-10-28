import { Table } from "@mantine/core";
import type { ReactNode } from "react";

export type Column<R> = { key: string; label: string; render: (r: R) => ReactNode };

// DataTable is the admin's list table: one header row, one row per item, and
// a full-width empty line when there are none.
export function DataTable<R>({ columns, rows, rowKey, empty }: { columns: Column<R>[]; rows: R[]; rowKey: (r: R) => string; empty: ReactNode }) {
  return (
    <Table striped highlightOnHover verticalSpacing={6}>
      <Table.Thead>
        <Table.Tr>{columns.map((c) => <Table.Th key={c.key}>{c.label}</Table.Th>)}</Table.Tr>
      </Table.Thead>
      <Table.Tbody>
        {rows.length === 0
          ? <Table.Tr><Table.Td colSpan={columns.length} c="dimmed">{empty}</Table.Td></Table.Tr>
          : rows.map((r) => <Table.Tr key={rowKey(r)}>{columns.map((c) => <Table.Td key={c.key}>{c.render(r)}</Table.Td>)}</Table.Tr>)}
      </Table.Tbody>
    </Table>
  );
}
