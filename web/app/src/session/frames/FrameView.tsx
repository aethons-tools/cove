import type { ReactNode } from "react";
import { useFrame } from "./useFrame";
import "./frames.css";

// FrameView is a framed view with its bar: lead (e.g. a back button), the
// status, Reload (reload), and Open in new tab (the mint's plain URL, else
// open). The iframe is keyed by mint, so a replaced frame is a new element
// and its late messages fail the source check.
export function FrameView({ sid, name, open, lead, reload }: {
  sid: string; name: string; open?: string; lead?: ReactNode; reload?: boolean;
}) {
  const { src, seq, status, reopen, open: minted, frame, start, loaded } = useFrame(sid, name);
  const href = minted || (open && /^https?:\/\//i.test(open) ? open : "");
  return (
    <div className="st-box">
      <div className="st-bar">
        {lead}
        <span className="st-status">{status}{reopen && <button type="button" className="st-reopen" onClick={start}>Reopen this view</button>}</span>
        <span style={{ flex: 1 }} />
        {reload && <button type="button" className="st-reload" onClick={start}>Reload</button>}
        {href && <a className="st-open" href={href} target="_blank" rel="noopener">Open in new tab</a>}
      </div>
      <div className="st-frame">
        {src && (
          <iframe key={seq} ref={frame} className="st-iframe" title={name} src={src}
            allow="clipboard-read; clipboard-write" referrerPolicy="origin" onLoad={loaded} />
        )}
      </div>
    </div>
  );
}
