// The /app/user/api client. Every error is an ApiError; a non-JSON refusal
// (the gate's HTML 403 or a login redirect) means the sign-in has expired.
export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message);
    this.name = "ApiError";
  }
}

export const API = "/app/user/api/";

export async function apiGet<T>(path: string): Promise<T> {
  let res: Response;
  try {
    res = await fetch(API + path, { headers: { Accept: "application/json" }, credentials: "same-origin", redirect: "manual" });
  } catch {
    throw new ApiError(0, "unavailable", "Jam can't be reached. Check your connection.");
  }
  const json = (res.headers.get("Content-Type") ?? "").startsWith("application/json");
  if (!json) {
    if (res.type === "opaqueredirect" || res.status === 401 || res.status === 403 || (res.status >= 300 && res.status < 400)) {
      throw new ApiError(res.status, "signed_out", "Your sign-in has expired.");
    }
    throw new ApiError(res.status, "unavailable", `Jam answered ${res.status}. Try again shortly.`);
  }
  const body: unknown = await res.json();
  if (!res.ok) {
    const e = body as { error?: string; code?: string };
    throw new ApiError(res.status, e.code ?? "error", e.error ?? res.statusText);
  }
  return body as T;
}
