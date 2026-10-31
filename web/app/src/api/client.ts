// The Jam app's API clients (/app/user/api, and the admin area's
// /app/admin/api through apiAt). Every error is an ApiError; a non-JSON refusal
// (the gate's HTML 403 or a login redirect) means the sign-in has expired.
import type { Method } from "../bridge/protocol";

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message);
    this.name = "ApiError";
  }
}

export const API = "/app/user/api/";

// shouldRetry is the queries' retry policy: an outage gets a few tries; an
// expired sign-in or a refusal (any 4xx) is final, so it shows at once.
export function shouldRetry(failureCount: number, error: unknown): boolean {
  if (error instanceof ApiError && (error.code === "signed_out" || (error.status >= 300 && error.status < 500))) return false;
  return failureCount < 3;
}

// statusText is the HTTP reason phrase, when the transport has one.
export type Raw = { status: number; statusText?: string; contentType: string; redirected: boolean; json?: unknown; text?: string };
export type TransportRequest = { method: Method; url: string; json?: unknown; form?: FormData; headers?: Record<string, string> };
// A Transport performs one request; it throws ApiError for transport-level failures.
export type Transport = (r: TransportRequest) => Promise<Raw>;

// interpret turns a raw response into the body or an ApiError.
export function interpret<T>(raw: Raw): T {
  if (raw.status === 204) return undefined as T;
  const json = raw.contentType.startsWith("application/json");
  if (!json) {
    if (raw.redirected || raw.status === 401 || raw.status === 403 || (raw.status >= 300 && raw.status < 400)) {
      throw new ApiError(raw.status, "signed_out", "Your sign-in has expired.");
    }
    throw new ApiError(raw.status, "unavailable", `Jam answered ${raw.status}. Try again shortly.`);
  }
  if (raw.status < 200 || raw.status >= 300) {
    const e = (raw.json ?? {}) as { error?: string; code?: string };
    throw new ApiError(raw.status, e.code ?? "error", e.error ?? raw.statusText ?? "");
  }
  return raw.json as T;
}

export const fetchTransport: Transport = async ({ method, url, json, form, headers: extra }) => {
  let res: Response;
  try {
    // A FormData body is sent as multipart (the browser sets its boundary).
    res = await fetch(url, {
      method,
      headers: { ...(json === undefined || form ? { Accept: "application/json" } : { Accept: "application/json", "Content-Type": "application/json" }), ...extra },
      body: form ?? (json === undefined ? undefined : JSON.stringify(json)),
      credentials: "same-origin",
      redirect: "manual",
    });
  } catch {
    throw new ApiError(0, "unavailable", "Jam can't be reached. Check your connection.");
  }
  const contentType = res.headers.get("Content-Type") ?? "";
  const raw: Raw = { status: res.status, statusText: res.statusText, contentType, redirected: res.type === "opaqueredirect" };
  if (res.status === 204) return raw;
  if (contentType.startsWith("application/json")) raw.json = await res.json();
  return raw;
};

let transport: Transport = fetchTransport;
// setTransport swaps how requests are sent; no argument restores fetchTransport.
export function setTransport(t?: Transport): void {
  transport = t ?? fetchTransport;
}

async function request<T>(base: string, method: Method, path: string, body?: unknown, headers?: () => Record<string, string>): Promise<T> {
  const form = body instanceof FormData;
  const req: TransportRequest = { method, url: base + path };
  const h = headers?.();
  if (h && Object.keys(h).length) req.headers = h;
  if (form) req.form = body;
  else if (body !== undefined) req.json = body;
  return interpret<T>(await transport(req));
}

// apiAt is a client for the API at base (a path ending in "/"); headers, when
// given, adds headers to every request (the admin console's config branch).
export function apiAt(base: string, headers?: () => Record<string, string>) {
  return {
    get: <T>(path: string) => request<T>(base, "GET", path, undefined, headers),
    post: <T = void>(path: string, body: unknown) => request<T>(base, "POST", path, body, headers),
    del: <T = void>(path: string, body?: unknown) => request<T>(base, "DELETE", path, body, headers),
    postForm: <T = void>(path: string, form: FormData) => request<T>(base, "POST", path, form, headers),
  };
}

const user = apiAt(API);
export const apiGet = user.get;
export const apiPost = user.post;

// POLL_MS is the live queries' fallback refresh (the old Inbox's 30 s poll),
// for when the event stream is down or absent.
export const POLL_MS = 30_000;
