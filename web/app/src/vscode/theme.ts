import { Badge, defaultVariantColorsResolver, mergeThemeOverrides, NavLink, type CSSVariablesResolver, type VariantColorsResolver } from "@mantine/core";
import { theme } from "../theme";

// The webview panes take their look from VS Code's theme, by the colours it
// gives every webview as --vscode-* variables, so they follow its theme
// switches. Only the primary colour is VS Code's: other colours (a session's
// phase, needs-you) carry meaning and keep Mantine's.

const vs = (name: string): string => `var(--vscode-${name})`;
const isPrimary = (color: string | undefined): boolean => color === undefined || color === theme.primaryColor;

// Filled buttons are VS Code's buttons, light ones its secondary buttons.
const variantColorResolver: VariantColorsResolver = (input) => {
  if (isPrimary(input.color) && input.variant === "filled") {
    return { background: vs("button-background"), hover: vs("button-hoverBackground"), color: vs("button-foreground"), border: "none" };
  }
  if (isPrimary(input.color) && input.variant === "light") {
    return { background: vs("button-secondaryBackground"), hover: vs("button-secondaryHoverBackground"), color: vs("button-secondaryForeground"), border: "none" };
  }
  return defaultVariantColorsResolver(input);
};

const colors: Record<string, string> = {
  "--mantine-color-text": vs("foreground"),
  "--mantine-color-dimmed": vs("descriptionForeground"),
  "--mantine-color-anchor": vs("textLink-foreground"),
  "--mantine-color-error": vs("errorForeground"),
  "--mantine-color-placeholder": vs("input-placeholderForeground"),
  "--mantine-color-default": vs("input-background"),
  "--mantine-color-default-hover": vs("list-hoverBackground"),
  "--mantine-color-default-color": vs("input-foreground"),
  "--mantine-color-default-border": `var(--vscode-input-border, ${vs("widget-border")})`,
  "--mantine-primary-color-filled": vs("button-background"),
  "--mantine-primary-color-filled-hover": vs("button-hoverBackground"),
  "--mantine-primary-color-contrast": vs("button-foreground"),
  "--mantine-primary-color-light": vs("button-secondaryBackground"),
  "--mantine-primary-color-light-hover": vs("button-secondaryHoverBackground"),
  "--mantine-primary-color-light-color": vs("button-secondaryForeground"),
};

// vscodeCssVariables is the same VS Code colours in either scheme: VS Code's
// theme already is the light or dark one.
export const vscodeCssVariables: CSSVariablesResolver = () => ({ variables: {}, light: colors, dark: colors });

export const vscodeTheme = mergeThemeOverrides(theme, {
  fontFamily: vs("font-family"),
  fontFamilyMonospace: vs("editor-font-family"),
  radius: { xs: "2px" },
  defaultRadius: "xs",
  variantColorResolver,
  components: {
    // The rail's rows: VS Code's list selection and hover.
    NavLink: NavLink.extend({
      vars: (_t, p) => (isPrimary(p.color) ? {
        root: { "--nl-bg": vs("list-activeSelectionBackground"), "--nl-color": vs("list-activeSelectionForeground"), "--nl-hover": vs("list-hoverBackground") },
        children: {},
      } : { root: {}, children: {} }),
    }),
    // Counts (unread) are VS Code's badges.
    Badge: Badge.extend({
      vars: (_t, p) => (isPrimary(p.color) && (p.variant ?? "filled") === "filled" ? {
        root: { "--badge-bg": vs("badge-background"), "--badge-color": vs("badge-foreground") },
      } : { root: {} }),
    }),
  },
});
