export type Uploader = (file: File, onProgress: (pct: number) => void) => { done: Promise<string>; abort: () => void };

// uploadOutcome reads an upload's answer: the ref of a 201, else the
// server's reason.
export function uploadOutcome(status: number, body: string): string | Error {
  let ref = "";
  try { ref = (JSON.parse(body) as { ref?: string }).ref ?? ""; } catch { /* a text refusal */ }
  if (status === 201 && ref) return ref;
  return new Error((body || `failed ${status}`).trim());
}

// xhrUpload sends one file to /app/user/uploads as a pending attachment (raw
// body; XHR for the progress events fetch lacks). done resolves its ref.
export const xhrUpload: Uploader = (file, onProgress) => {
  const xhr = new XMLHttpRequest();
  const done = new Promise<string>((resolve, reject) => {
    xhr.open("POST", "/app/user/uploads");
    xhr.setRequestHeader("Content-Type", file.type || "application/octet-stream");
    xhr.setRequestHeader("X-Filename", encodeURIComponent(file.name));
    xhr.upload.onprogress = (ev) => { if (ev.lengthComputable) onProgress((100 * ev.loaded) / ev.total); };
    xhr.onload = () => {
      const out = uploadOutcome(xhr.status, xhr.responseText);
      if (typeof out === "string") resolve(out);
      else reject(out);
    };
    // An early 413 often closes the connection mid-send: say so.
    xhr.onerror = xhr.ontimeout = () => reject(new Error("upload failed (too large, or the connection dropped)"));
    xhr.onabort = () => reject(new DOMException("aborted", "AbortError"));
    xhr.send(file);
  });
  return { done, abort: () => xhr.abort() };
};

let uploader: Uploader = xhrUpload;
export function setUploader(u?: Uploader): void { uploader = u ?? xhrUpload; }
export function upload(...args: Parameters<Uploader>): ReturnType<Uploader> { return uploader(...args); }
