import { Alert, Button, Group, Stack } from "@mantine/core";
import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Cancelled } from "./cancelled";
import { Confirm } from "./Confirm";

// EditForm is an admin write form: its submit shows progress (and can't send
// twice), asks first when confirm is set, and shows a refusal inline, keeping
// what was typed.
export function EditForm({ submit, onSubmit, confirm, resetOnSuccess, children }: {
  submit: string; onSubmit: () => Promise<unknown>; confirm?: string; resetOnSuccess?: () => void; children?: ReactNode;
}) {
  const [busy, setBusy] = useState(false);
  const [asking, setAsking] = useState(false);
  const [error, setError] = useState("");
  const running = useRef(false);
  const run = async () => {
    if (running.current) return;
    running.current = true;
    setBusy(true);
    setAsking(false);
    try {
      await onSubmit();
      setError("");
      resetOnSuccess?.();
    } catch (e: unknown) {
      if (!(e instanceof Cancelled)) setError(e instanceof Error ? e.message : String(e));
    } finally {
      running.current = false;
      setBusy(false);
    }
  };
  const go = (e: FormEvent) => {
    e.preventDefault();
    if (confirm) setAsking(true); else void run();
  };
  return (
    <form onSubmit={go}>
      <Stack gap="xs">
        {children}
        {error && <Alert color="red" p="xs">{error}</Alert>}
        <Group><Button type="submit" size="xs" loading={busy} disabled={busy}>{submit}</Button></Group>
      </Stack>
      {confirm && <Confirm opened={asking} text={confirm} action="Confirm" onCancel={() => setAsking(false)} onConfirm={() => void run()} />}
    </form>
  );
}
