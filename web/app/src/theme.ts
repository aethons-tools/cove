import { createTheme, type MantineColorsTuple } from "@mantine/core";

// jam.css's accent (#B5306A) as a Mantine palette, index 6 = the accent.
const jam: MantineColorsTuple = ["#F9EEF3", "#F0D6E1", "#E5B7CB", "#DA98B4", "#CD729A", "#C04F80", "#B5306A", "#9F2A5D", "#8A2451", "#741F44"];

export const theme = createTheme({
  primaryColor: "jam",
  colors: { jam },
  fontFamily: '"IBM Plex Sans", ui-sans-serif, system-ui, sans-serif',
  fontFamilyMonospace: '"IBM Plex Mono", ui-monospace, Menlo, monospace',
  fontSizes: { xs: "12px", sm: "13px", md: "14px", lg: "16px", xl: "18px" },
  defaultRadius: "md",
});
