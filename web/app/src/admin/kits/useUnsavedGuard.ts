import { useEffect, type RefObject } from "react";
import { useBlocker } from "react-router";

export const unsavedAsk = "You have unsaved changes in the editor. Discard them?";

// useUnsavedGuard holds navigation (in the app: a blocker the caller asks
// about; leaving the page: the browser's own prompt) while dirty.current.
export function useUnsavedGuard(dirty: RefObject<boolean>) {
  const blocker = useBlocker(({ currentLocation, nextLocation }) =>
    dirty.current && (currentLocation.pathname !== nextLocation.pathname || currentLocation.search !== nextLocation.search));
  useEffect(() => {
    const ask = (e: BeforeUnloadEvent) => { if (dirty.current) e.preventDefault(); };
    window.addEventListener("beforeunload", ask);
    return () => window.removeEventListener("beforeunload", ask);
  }, [dirty]);
  return blocker;
}
