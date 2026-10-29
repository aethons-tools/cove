// The Jam API client: bearer auth, no redirects, and a 401 means signed out.
import type { Conversation, RailResponse, SessionHead } from "../../app/src/api/types.js" with { "resolution-mode": "import" };

export class SignedOutError extends Error {
  constructor() {
    super("Your Jam sign-in expired or was revoked.");
    this.name = "SignedOutError";
  }
}

export class ApiError extends Error {
  constructor(
    message: string,
    public status: number,
    public code: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

const seg = encodeURIComponent;

export class JamApi {
  constructor(
    private base: string,
    private token: string,
    private doFetch: typeof globalThis.fetch,
  ) {}

  sessionHead(id: string): Promise<SessionHead> {
    return this.req("GET", `/app/user/api/sessions/${seg(id)}`);
  }
  rail(): Promise<RailResponse> {
    return this.req("GET", "/app/user/api/rail");
  }
  conversation(c: string): Promise<Conversation> {
    return this.req("GET", `/app/user/api/conversations/${seg(c)}`);
  }
  async issueCert(studio: string, publicKey: string): Promise<{ certificate: string; expiresAt: string }> {
    const r = await this.req<{ certificate: string; expires_at: string }>(
      "POST",
      `/me/studios/${seg(studio)}/ssh/cert`,
      { public_key: publicKey },
    );
    return { certificate: r.certificate, expiresAt: r.expires_at };
  }

  private async req<T>(method: string, path: string, body?: unknown): Promise<T> {
    const headers: Record<string, string> = { Authorization: `Bearer ${this.token}`, Accept: "application/json" };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    const res = await this.doFetch(this.base + path, {
      method,
      headers,
      redirect: "error",
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (res.status === 401) throw new SignedOutError();
    if (!res.ok) {
      const text = (await res.text()).trim();
      let msg = text;
      let code = "";
      try {
        const j = JSON.parse(text) as { error?: unknown; code?: unknown };
        if (typeof j.error === "string") msg = j.error;
        if (typeof j.code === "string") code = j.code;
      } catch {
        // a text body is the message
      }
      throw new ApiError(msg || `HTTP ${res.status}`, res.status, code);
    }
    return (await res.json()) as T;
  }
}
