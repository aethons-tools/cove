// upload sends one file to /app/user/uploads as a pending attachment (raw
// body; XHR for the progress events fetch lacks). done resolves its ref.
export function upload(file: File, onProgress: (pct: number) => void): { done: Promise<string>; abort: () => void } {
  const xhr = new XMLHttpRequest();
  const done = new Promise<string>((resolve, reject) => {
    xhr.open("POST", "/app/user/uploads");
    xhr.setRequestHeader("Content-Type", file.type || "application/octet-stream");
    xhr.setRequestHeader("X-Filename", encodeURIComponent(file.name));
    xhr.upload.onprogress = (ev) => { if (ev.lengthComputable) onProgress((100 * ev.loaded) / ev.total); };
    xhr.onload = () => {
      let ref = "";
      try { ref = (JSON.parse(xhr.responseText) as { ref?: string }).ref ?? ""; } catch { /* a text refusal */ }
      if (xhr.status === 201 && ref) resolve(ref);
      else reject(new Error((xhr.responseText || `failed ${xhr.status}`).trim()));
    };
    // An early 413 often closes the connection mid-send: say so.
    xhr.onerror = xhr.ontimeout = () => reject(new Error("upload failed (too large, or the connection dropped)"));
    xhr.onabort = () => reject(new DOMException("aborted", "AbortError"));
    xhr.send(file);
  });
  return { done, abort: () => xhr.abort() };
}
