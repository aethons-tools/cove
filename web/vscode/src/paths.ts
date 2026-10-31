// Where the Jam CLI keeps its files; the extension shares them so the CLI
// and the extension see the same key, certificates and token.
import * as path from "node:path";

// configDir matches cmd/jam/clientconfig.go:configDir.
export function configDir(env: NodeJS.ProcessEnv, home: string): string {
  const x = env.XDG_CONFIG_HOME;
  return path.join(x ? x : path.join(home, ".config"), "jam");
}
export const sshDir = (cfg: string): string => path.join(cfg, "ssh");
export const keyPath = (cfg: string): string => path.join(sshDir(cfg), "id_ed25519");
export const certPath = (cfg: string, studio: string): string => path.join(sshDir(cfg), `${studio}-cert.pub`);
// appTokenPath is the jam CLI's token file for profile app (--app; "default").
export const appTokenPath = (cfg: string, app = "default"): string => path.join(cfg, `${app}-app-token.json`);
export const sshConfigFile = (home: string): string => path.join(home, ".ssh", "config");
export const knownHostsFile = (home: string): string => path.join(home, ".ssh", "known_hosts");
