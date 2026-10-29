// Quoting helpers ported from cmd/jam/ssh.go (shellQuote, proxyBinary,
// sshConfigEscape, sshConfigPath); the output must stay byte-identical.

// shellQuote single-quotes s for the shell OpenSSH runs a ProxyCommand with.
export function shellQuote(s: string): string {
  return "'" + s.replaceAll("'", `'\\''`) + "'";
}

// proxyBinary is the jam path, shell-quoted when it has characters outside
// [A-Za-z0-9/._+-].
export function proxyBinary(absPath: string): string {
  return /[^A-Za-z0-9/._+-]/.test(absPath) ? shellQuote(absPath) : absPath;
}

// sshConfigEscape escapes ssh_config's %-tokens.
export function sshConfigEscape(s: string): string {
  return s.replaceAll("%", "%%");
}

// sshConfigPath renders a path as an ssh_config value.
export function sshConfigPath(p: string): string {
  const e = sshConfigEscape(p);
  return /[ \t]/.test(e) ? `"${e}"` : e;
}
