import type { ReactNode } from "react";
import { SessionHeader } from "./SessionHeader";
import { useSessionHead } from "./useSession";

// SessionView: a session's home channel — its header above the
// conversation's header and messages.
export function SessionView({ id, header, children }: {
  id: string; channel: string; linked: string | null; onAsk: (id: string) => void; header: ReactNode; children: ReactNode;
}) {
  const q = useSessionHead(id);
  return (
    <>
      {q.data && <SessionHeader head={q.data} tab={null} onTab={() => {}} />}
      {header}
      {children}
    </>
  );
}
