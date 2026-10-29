import { Alert, Anchor } from "@mantine/core";
import { useContext } from "react";
import type { ApiError } from "./api/client";
import { HostContext } from "./host";

// Problem shows an API failure. An expired sign-in links back to here (the
// current page), through the gate: it answers with a login redirect carrying
// return_to, or with its own "Sign in again" page (via logout) when only one
// of the two tokens expired — so the app never builds login URLs itself.
export function Problem({ error, here }: { error: ApiError; here: string }) {
  const { signIn } = useContext(HostContext);
  return (
    <Alert color={error.code === "signed_out" ? "yellow" : "red"} m="sm">
      {error.message}{" "}
      {error.code === "signed_out" && (signIn
        ? <Anchor component="button" onClick={signIn}>Sign in again</Anchor>
        : <Anchor href={here}>Sign in again</Anchor>)}
    </Alert>
  );
}
