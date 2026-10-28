import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MantineProvider } from "@mantine/core";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import { useState } from "react";
import { server } from "../../test/server";
import { theme } from "../../theme";
import { TypeAhead, tokenAt } from "./TypeAhead";

describe("tokenAt", () => {
  it("is the token around the caret", () => {
    const v = "user:alice@15m\nchannel:eng";
    expect(tokenAt(v, 10, true)).toEqual({ start: 0, end: 10, text: "user:alice" });
    expect(tokenAt(v, v.length, true)).toEqual({ start: 15, end: v.length, text: "channel:eng" });
    expect(tokenAt("a, bo,c", 5, true)).toEqual({ start: 2, end: 5, text: " bo" });
  });

  it("never completes a timeout", () => {
    expect(tokenAt("user:alice@15", 13, true)).toBeNull();
  });

  it("is the whole line without list", () => {
    expect(tokenAt("ab,cd", 3, false)).toEqual({ start: 0, end: 5, text: "ab,cd" });
  });
});

function Box({ initial }: { initial: string }) {
  const [v, setV] = useState(initial);
  return <TypeAhead label="Tiers" kind="targets" list multiline value={v} onChange={setV} />;
}

describe("TypeAhead", () => {
  it("completes the token at the caret, not at the end", async () => {
    server.use(http.get("/app/admin/suggest", () => HttpResponse.json(["user:alice", "user:bob", "channel:eng"])));
    const user = userEvent.setup();
    render(<MantineProvider theme={theme} env="test"><QueryClientProvider client={new QueryClient()}><Box initial={"user:alice@15m\n"} /></QueryClientProvider></MantineProvider>);
    const box = screen.getByLabelText("Tiers") as HTMLTextAreaElement;
    await user.type(box, ",user:b", { initialSelectionStart: 10, initialSelectionEnd: 10 });
    await user.click(await screen.findByRole("option", { name: "user:bob" }));
    expect(box).toHaveValue("user:alice,user:bob@15m\n");
  });
});
