import "@mantine/core/styles.css";
import "@mantine/notifications/styles.css";
import "@fontsource/ibm-plex-sans/400.css";
import "@fontsource/ibm-plex-sans/600.css";
import "@fontsource/ibm-plex-mono/400.css";
import { start } from "./start";

// One bundle, two areas: the admin area is its own lazily loaded chunk, so
// the user area's initial load doesn't carry it.
const root = document.getElementById("root")!;
if (location.pathname.startsWith("/app/admin/")) {
  void import("./admin/start").then((m) => m.start(root));
} else {
  void start(root);
}
