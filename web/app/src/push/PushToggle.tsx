import { Menu } from "@mantine/core";
import { useEffect, useState } from "react";
import type { Bootstrap } from "../api/types";
import { realEnv, supported, syncOnLoad, turnOff, turnOn, view } from "./push";

// PushToggle is the principal menu's per-device Notifications item (the old
// push.js): On subscribes this browser and stores it with Jam; Off removes it.
export function PushToggle({ boot }: { boot: Bootstrap }) {
  const [env] = useState(realEnv);
  const [on, setOn] = useState(false);
  const [busy, setBusy] = useState(() => supported(env));
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    if (!supported(env)) return;
    let live = true;
    syncOnLoad(env, boot.user.id, boot.pushKey)
      .then((v) => { if (live) setOn(v); }, () => { /* leave it Off */ })
      .finally(() => { if (live) setBusy(false); });
    return () => { live = false; };
  }, [env, boot.user.id, boot.pushKey]);
  const v = view(env, on, busy);
  if (!v) return null;
  const click = async () => {
    setBusy(true);
    setFailed(false);
    try {
      if (on) {
        await turnOff(env, boot.user.id);
        setOn(false);
      } else setOn(await turnOn(env, boot.user.id, boot.pushKey));
    } catch {
      setFailed(true);
    }
    setBusy(false);
  };
  return (
    <Menu.Item disabled={v.disabled} closeMenuOnClick={false} aria-pressed={v.on}
      title={failed ? "Could not change notifications for this device" : undefined} onClick={() => void click()}>
      {v.label}
    </Menu.Item>
  );
}
