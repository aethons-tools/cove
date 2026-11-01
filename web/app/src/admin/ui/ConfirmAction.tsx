import { Button } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useRef, useState, type ReactNode } from "react";
import { useConfigEditable } from "../branch/branch";
import { Cancelled } from "./cancelled";
import { Confirm } from "./Confirm";

// ConfirmAction is a destructive (or weighty) admin action behind a confirm:
// it runs once per confirmation and shows a refusal as a notification. Like
// EditForm, it renders only on a branch unless live.
export function ConfirmAction({ label, confirm, onConfirm, color = "red", size = "xs", variant, disabled, title, trigger, ariaLabel, live }: {
  label: string; confirm: string; onConfirm: () => Promise<unknown>; color?: string; size?: string; variant?: string; disabled?: boolean; title?: string;
  trigger?: ReactNode; ariaLabel?: string; // the button's own text (default label) and accessible name
  live?: boolean;
}) {
  const editable = useConfigEditable();
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
      if (e instanceof Cancelled) return;
      // A refusal stays until dismissed: it says what to do next.
      notifications.show({ color: "red", message: e instanceof Error ? e.message : String(e), autoClose: false });
    } finally {
      running.current = false;
      setBusy(false);
    }
  };
  if (!live && !editable) return null;
  return (
    <>
      <Button size={size} color={color} variant={variant ?? "light"} loading={busy} disabled={busy || disabled} title={title} aria-label={ariaLabel} onClick={() => setAsking(true)}>{trigger ?? label}</Button>
      <Confirm opened={asking} text={confirm} action={label} color={color} busy={busy} onCancel={() => setAsking(false)} onConfirm={() => void run()} />
    </>
  );
}
