import { beforeEach, describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MantineProvider } from "@mantine/core";
import { theme } from "../theme";
import { SoundsSettings } from "./SoundsSettings";

beforeEach(() => localStorage.clear());

describe("SoundsSettings", () => {
  it("saves a changed chime", async () => {
    render(<MantineProvider theme={theme}><SoundsSettings opened onClose={() => {}} /></MantineProvider>);
    await userEvent.selectOptions(await screen.findByRole("combobox", { name: "Messages" }), "bell");
    expect(JSON.parse(localStorage.getItem("jam-sounds")!).messages).toBe("bell");
    expect(screen.getByText(/With Jam closed, notifications use your system's sound/)).toBeInTheDocument();
  });
});
