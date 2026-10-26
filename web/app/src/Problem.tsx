import { Alert, Anchor } from "@mantine/core";
import type { ApiError } from "./api/client";

// Problem shows an API failure. An expired sign-in links back to here (the
// current page), through the gate: it answers with a login redirect carrying
// return_to, or with its own "Sign in again" page (via logout) when only one
// of the two tokens expired — so the app never builds login URLs itself.
export function Problem({ error, here }: { error: ApiError; here: string }) {
  return (
    <Alert color={error.code === "signed_out" ? "yellow" : "red"} m="sm">
      {error.message}{" "}
      {error.code === "signed_out" && <Anchor href={here}>Sign in again</Anchor>}
    </Alert>
  );
}
