// A kit draft's problems (docs/usage/jam/kits-drafts.md#the-draft) in the
// Problems view: each on the file it starts by naming (`<path>: …`), else on
// kit.yml.
import * as vscode from "vscode";
import type { DraftFiles } from "../../../app/src/admin/api/types.js" with { "resolution-mode": "import" };
import { type KitRef, kitKey, kitUri } from "./uri";

export function attachProblems(): (ref: KitRef, files: DraftFiles) => void {
  const coll = vscode.languages.createDiagnosticCollection("jam-kits");
  const shown = new Map<string, vscode.Uri[]>();
  return (ref, files) => {
    for (const u of shown.get(kitKey(ref)) ?? []) coll.delete(u);
    const by = new Map<string, vscode.Diagnostic[]>();
    for (const p of files.problems) {
      const named = files.files.find((f) => p.startsWith(`${f.path}:`));
      const path = named?.path ?? "kit.yml";
      const d = new vscode.Diagnostic(new vscode.Range(0, 0, 0, 0), p, vscode.DiagnosticSeverity.Error);
      d.source = "Jam";
      by.set(path, [...(by.get(path) ?? []), d]);
    }
    const uris: vscode.Uri[] = [];
    for (const [path, ds] of by) {
      const u = kitUri(ref.jam, ref.branch, ref.kit, path);
      coll.set(u, ds);
      uris.push(u);
    }
    shown.set(kitKey(ref), uris);
  };
}
