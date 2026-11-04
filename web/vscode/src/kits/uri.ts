// jamkit: URIs name a file of a kit's draft on a config branch of one Jam:
// jamkit://<jam slug>/<branch>/<kit>/<path>. A branch's "/" is written "~"
// (never in a git branch name), since VS Code decodes a URI's path and an
// escaped "/" would not survive.
import * as vscode from "vscode";

export const KIT_SCHEME = "jamkit";

export type KitRef = { jam: string; branch: string; kit: string };
export type KitPath = KitRef & { path: string };

export function kitUri(jam: string, branch: string, kit: string, path = ""): vscode.Uri {
  const tail = path ? `/${path}` : "";
  return vscode.Uri.from({ scheme: KIT_SCHEME, authority: jam, path: `/${branch.replaceAll("/", "~")}/${kit}${tail}` });
}

// parseKitUri is uri's parts; undefined when it isn't a kit URI.
export function parseKitUri(uri: vscode.Uri): KitPath | undefined {
  if (uri.scheme !== KIT_SCHEME) return undefined;
  const [, branch, kit, ...rest] = uri.path.split("/");
  if (!branch || !kit) return undefined;
  return { jam: uri.authority, branch: branch.replaceAll("~", "/"), kit, path: rest.filter((s) => s !== "").join("/") };
}

// kitKey identifies a kit's draft across URIs.
export const kitKey = (r: KitRef): string => `${r.jam}\n${r.branch}\n${r.kit}`;
