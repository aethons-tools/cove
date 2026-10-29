import { useEffect } from "react";
import { useLocation, useNavigate } from "react-router";

// NavBridge keeps the pane's memory router on home: a navigation anywhere
// else (a Link the click interceptor missed, New whiteboard's navigate) is
// handed to onLeave, then replaced back. The pane's one route matches every
// path, so what it shows stays mounted throughout.
export function NavBridge({ home, onLeave }: { home: string; onLeave: (path: string) => void }): null {
  const loc = useLocation();
  const go = useNavigate();
  useEffect(() => {
    if (loc.pathname === home) return;
    onLeave(loc.pathname + loc.search + loc.hash);
    void go(home, { replace: true });
  }, [loc, home, onLeave, go]);
  return null;
}
