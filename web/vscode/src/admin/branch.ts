// The config branch kit editing works on, remembered per Jam
// (docs/usage/jam/vscode.md#editing-kits). "" = not chosen: the Jam's
// default name for the operator (jam/<operator>).
export interface MementoLike {
  get<T>(key: string): T | undefined;
  update(key: string, value: unknown): Thenable<void> | Promise<void>;
}

const key = (jamUrl: string) => `jam.configBranch:${jamUrl}`;

export class BranchState {
  constructor(private m: MementoLike) {}
  get(jamUrl: string): string {
    return this.m.get<string>(key(jamUrl)) ?? "";
  }
  async set(jamUrl: string, name: string): Promise<void> {
    await this.m.update(key(jamUrl), name);
  }
}
