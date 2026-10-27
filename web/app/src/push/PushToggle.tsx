import { Menu } from "@mantine/core";
import type { PushState } from "./usePush";

// PushToggle is the principal menu's per-device Notifications item; its
// state lives in the title bar (usePush), so it outlives the menu closing.
export function PushToggle({ push }: { push: PushState }) {
  const v = push.view;
  if (!v) return null;
  return (
    <Menu.Item disabled={v.disabled} closeMenuOnClick={false} aria-pressed={v.on}
      title={push.failed ? "Could not change notifications for this device" : undefined} onClick={push.toggle}>
      {v.label}
    </Menu.Item>
  );
}
