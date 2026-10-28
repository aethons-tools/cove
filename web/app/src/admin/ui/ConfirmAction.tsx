import { Button } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useRef, useState } from "react";
import { Confirm } from "./Confirm";

// ConfirmAction is a destructive (or weighty) admin action behind a confirm:
// it runs once per confirmation and shows a refusal as a notification.
export function ConfirmAction({ label, confirm, onConfirm, color = "red", size = "xs", variant }: {
  label: string; confirm: string; onConfirm: () => Promise<unknown>; color?: string; size?: string; variant?: string;
}) {
  const [asking, setAsking] = useState(false);
  const [busy, setBusy] = useState(false);
  const running = useRef(false);
  const run = async () => {
    if (running.current) return;
    running.current = true;
    setBusy(true);
    try {
      await onConfirm();
      setAsking(false);
    } catch (e: unknown) {
      setAsking(false);
      notifications.show({ color: "red", message: e instanceof Error ? e.message : String(e) });
    } finally {
      running.current = false;
      setBusy(false);
    }
  };
  return (
    <>
      <Button size={size} color={color} variant={variant ?? "light"} loading={busy} disabled={busy} onClick={() => setAsking(true)}>{label}</Button>
      <Confirm opened={asking} text={confirm} action={label} color={color} busy={busy} onCancel={() => setAsking(false)} onConfirm={() => void run()} />
    </>
  );
}
