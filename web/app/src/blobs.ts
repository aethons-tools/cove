import { useEffect, useState } from "react";

// A BlobResolver turns a user-area blob path into a blob: URL, for shells
// that cannot let an <img> fetch it directly (the VS Code webview).
export type BlobResolver = (path: string) => Promise<string>;
let resolver: BlobResolver | undefined;
export function setBlobResolver(r?: BlobResolver): void { resolver = r; }

// useBlobUrl is the path itself, synchronously, with no resolver; else
// undefined until the resolver answers (and still undefined if it fails).
export function useBlobUrl(path: string): string | undefined {
  const r = resolver;
  const [url, setUrl] = useState<{ path: string; url: string }>();
  useEffect(() => {
    if (!r) return;
    let live = true;
    r(path).then((u) => { if (live) setUrl({ path, url: u }); }, () => {});
    return () => { live = false; };
  }, [r, path]);
  if (!r) return path;
  return url?.path === path ? url.url : undefined;
}
