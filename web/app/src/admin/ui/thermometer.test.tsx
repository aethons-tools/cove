import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MantineProvider } from "@mantine/core";
import { theme } from "../../theme";
import { Thermometer, thermometerState } from "./Thermometer";

const show = (ui: React.ReactElement) => render(<MantineProvider theme={theme} env="test">{ui}</MantineProvider>);

describe("thermometerState", () => {
  it("is ok, then warning near the limit, then over past it", () => {
    expect(thermometerState(312, 1200)).toBe("ok");
    expect(thermometerState(1020, 1200)).toBe("warning");
    expect(thermometerState(1200, 1200)).toBe("warning");
    expect(thermometerState(1201, 1200)).toBe("over");
    expect(thermometerState(50, 100, 0.5)).toBe("warning");
  });
});

describe("Thermometer", () => {
  it("shows the count against the limit", () => {
    const { container } = show(<Thermometer used={312} limit={1200} label="one line per resource" />);
    expect(screen.getByText("312 / 1,200 bytes")).toBeInTheDocument();
    expect(screen.getByText("one line per resource")).toBeInTheDocument();
    expect(container.querySelector("[data-state]")).toHaveAttribute("data-state", "ok");
  });

  it("warns near the limit and is red over it", () => {
    const { container, rerender } = show(<Thermometer used={1100} limit={1200} />);
    expect(container.querySelector("[data-state]")).toHaveAttribute("data-state", "warning");
    rerender(<MantineProvider theme={theme} env="test"><Thermometer used={1300} limit={1200} unit="KiB" label="Over budget" /></MantineProvider>);
    expect(container.querySelector("[data-state]")).toHaveAttribute("data-state", "over");
    expect(screen.getByText("1,300 / 1,200 KiB")).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "100");
  });
});
