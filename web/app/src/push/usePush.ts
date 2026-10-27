import { useCallback, useEffect, useState } from "react";
import type { Bootstrap } from "../api/types";
import { realEnv, supported, syncOnLoad, turnOff, turnOn, view, type PushView } from "./push";

export interface PushState { view: PushView; failed: boolean; toggle(): void }

// usePush is this device's Web Push state (the old push.js), held by the
// always-mounted title bar: at page load it re-syncs the subscription (once
// per page session), so a shared device's endpoint belongs to whoever is
// signed in now; toggle turns it on or off. off: Web Push isn't configured.
export function usePush(boot: Bootstrap): PushState {
  const off = !boot.pushKey;
  const [env] = useState(realEnv);
  const [on, setOn] = useState(false);
  const [busy, setBusy] = useState(() => !off && supported(env));
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    if (off || !supported(env)) return;
    let live = true;
    syncOnLoad(env, boot.user.id, boot.pushKey)
      .then((v) => { if (live) setOn(v); }, () => { /* leave it Off */ })
      .finally(() => { if (live) setBusy(false); });
    return () => { live = false; };
  }, [off, env, boot.user.id, boot.pushKey]);
  const toggle = useCallback(() => {
    if (busy) return;
    setBusy(true);
    setFailed(false);
    void (async () => {
      try {
        if (on) {
          await turnOff(env, boot.user.id);
          setOn(false);
        } else setOn(await turnOn(env, boot.user.id, boot.pushKey));
      } catch {
        setFailed(true);
      }
      setBusy(false);
    })();
  }, [busy, on, env, boot.user.id, boot.pushKey]);
  return { view: off ? null : view(env, on, busy), failed, toggle };
}
