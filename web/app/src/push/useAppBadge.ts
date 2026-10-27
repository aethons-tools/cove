import { useEffect, useRef } from "react";

type BadgeNav = Navigator & { setAppBadge?: (n: number) => Promise<void>; clearAppBadge?: () => Promise<void> };

// useAppBadge shows n (the rail's unread total) on the installed app's icon
// and clears it at 0 (layout.html's meBadge); unsupported browsers skip it.
export function useAppBadge(n: number | undefined) {
  const shown = useRef<number | undefined>(undefined);
  useEffect(() => {
    if (n === undefined || n === shown.current) return;
    shown.current = n;
    const nav = navigator as BadgeNav;
    try {
      if (n > 0) nav.setAppBadge?.(n).catch(() => {});
      else nav.clearAppBadge?.().catch(() => {});
    } catch { /* unsupported */ }
  }, [n]);
}
