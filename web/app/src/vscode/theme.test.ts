import { describe, expect, it } from "vitest";
import { defaultVariantColorsResolver } from "@mantine/core";
import { vscodeCssVariables, vscodeTheme } from "./theme";

const resolve = (variant: string, color?: string) => vscodeTheme.variantColorResolver!({ variant, color, theme: vscodeTheme as never });

describe("the VS Code theme", () => {
  it("uses VS Code's font and near-square corners", () => {
    expect(vscodeTheme.fontFamily).toBe("var(--vscode-font-family)");
    expect(vscodeTheme.fontFamilyMonospace).toBe("var(--vscode-editor-font-family)");
    expect(vscodeTheme.defaultRadius).toBe("xs");
    expect(vscodeTheme.radius?.xs).toBe("2px");
  });

  it("primary filled buttons are VS Code's buttons", () => {
    expect(resolve("filled")).toMatchObject({
      background: "var(--vscode-button-background)",
      hover: "var(--vscode-button-hoverBackground)",
      color: "var(--vscode-button-foreground)",
    });
  });

  it("primary light buttons are VS Code's secondary buttons", () => {
    expect(resolve("light")).toMatchObject({
      background: "var(--vscode-button-secondaryBackground)",
      hover: "var(--vscode-button-secondaryHoverBackground)",
      color: "var(--vscode-button-secondaryForeground)",
    });
  });

  it("other colours keep Mantine's (status colours carry meaning)", () => {
    const red = resolve("filled", "red");
    expect(red).toEqual(defaultVariantColorsResolver({ variant: "filled", color: "red", theme: vscodeTheme as never }));
  });

  it("the CSS variables take text, dimmed, links and inputs from VS Code, in both schemes", () => {
    const v = vscodeCssVariables(vscodeTheme as never);
    for (const s of [v.light, v.dark]) {
      expect(s).toMatchObject({
        "--mantine-color-text": "var(--vscode-foreground)",
        "--mantine-color-dimmed": "var(--vscode-descriptionForeground)",
        "--mantine-color-anchor": "var(--vscode-textLink-foreground)",
        "--mantine-color-default": "var(--vscode-input-background)",
        "--mantine-primary-color-filled": "var(--vscode-button-background)",
      });
    }
  });

  it("the rail's active row is VS Code's list selection, and badges its badges", () => {
    const nl = (vscodeTheme.components!.NavLink as { vars: (t: unknown, p: unknown) => { root: Record<string, string> } }).vars(vscodeTheme, {});
    expect(nl.root).toMatchObject({
      "--nl-bg": "var(--vscode-list-activeSelectionBackground)",
      "--nl-color": "var(--vscode-list-activeSelectionForeground)",
      "--nl-hover": "var(--vscode-list-hoverBackground)",
    });
    const badge = (vscodeTheme.components!.Badge as { vars: (t: unknown, p: unknown) => { root: Record<string, string> } }).vars(vscodeTheme, {});
    expect(badge.root).toMatchObject({ "--badge-bg": "var(--vscode-badge-background)", "--badge-color": "var(--vscode-badge-foreground)" });
    const red = (vscodeTheme.components!.Badge as { vars: (t: unknown, p: unknown) => { root: Record<string, string> } }).vars(vscodeTheme, { color: "red" });
    expect(red.root).toEqual({});
  });
});
