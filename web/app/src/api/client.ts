// The /app/user/api client. Every error is an ApiError; a non-JSON refusal
// (the gate's HTML 403 or a login redirect) means the sign-in has expired.
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

async function request<T>(method: "GET" | "POST", path: string, body?: unknown): Promise<T> {
  let res: Response;
  try {
    res = await fetch(API + path, {
      method,
      headers: body === undefined ? { Accept: "application/json" } : { Accept: "application/json", "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: "same-origin",
      redirect: "manual",
    });
  } catch {
    throw new ApiError(0, "unavailable", "Jam can't be reached. Check your connection.");
  }
  if (res.status === 204) return undefined as T;
  const json = (res.headers.get("Content-Type") ?? "").startsWith("application/json");
  if (!json) {
    if (res.type === "opaqueredirect" || res.status === 401 || res.status === 403 || (res.status >= 300 && res.status < 400)) {
      throw new ApiError(res.status, "signed_out", "Your sign-in has expired.");
    }
    throw new ApiError(res.status, "unavailable", `Jam answered ${res.status}. Try again shortly.`);
  }
  const data: unknown = await res.json();
  if (!res.ok) {
    const e = data as { error?: string; code?: string };
    throw new ApiError(res.status, e.code ?? "error", e.error ?? res.statusText);
  }
  return data as T;
}

export const apiGet = <T>(path: string) => request<T>("GET", path);
export const apiPost = <T = void>(path: string, body: unknown) => request<T>("POST", path, body);

// POLL_MS is the live queries' fallback refresh (the old Inbox's 30 s poll),
// for when the event stream is down or absent.
export const POLL_MS = 30_000;
