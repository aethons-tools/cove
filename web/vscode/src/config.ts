// The extension's two settings, both application-scoped (package.json) so a
// workspace's .vscode/settings.json can't point the token at another Jam or
// run another binary.

const LOOPBACK = new Set(["localhost", "127.0.0.1", "[::1]"]);

// normalizeJamUrl is raw's origin (no trailing slash), or an Error saying
// what is wrong. It must be https:, except http: for a loopback host.
export function normalizeJamUrl(raw: string): string | Error {
  const s = raw.trim();
  if (s === "") return new Error("Set jam.url to your Jam's address");
  let u: URL;
  try {
    u = new URL(s);
  } catch {
    return new Error(`jam.url is not a URL: ${s}`);
  }
  if (u.username !== "" || u.password !== "") return new Error("jam.url must not carry a user name or password");
  const ok = u.protocol === "https:" || (u.protocol === "http:" && LOOPBACK.has(u.hostname));
  if (!ok || u.host === "") return new Error(`jam.url must be an https:// address (http:// only for localhost): ${s}`);
  return u.origin;
}

// settings reads the extension's settings through get (the "jam" section's
// getter).
export function settings(get: (key: string) => string | undefined): { url: string | Error; cliPath: string } {
  return { url: normalizeJamUrl(get("url") ?? ""), cliPath: (get("cliPath") ?? "").trim() };
}
