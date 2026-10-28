import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MantineProvider } from "@mantine/core";
import { theme } from "../../theme";
import { Facts } from "./Facts";

describe("Facts", () => {
  it("shows a missing value's unset text, dimmed", () => {
    render(<MantineProvider theme={theme} env="test"><Facts rows={[["Route", "/gh/"], ["Note", null, "none"]]} /></MantineProvider>);
    expect(screen.getByText("/gh/")).toBeInTheDocument();
    expect(screen.getByText("none")).toHaveAttribute("data-unset", "true");
  });
});
