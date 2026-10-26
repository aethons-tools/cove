import { Loader, Text } from "@mantine/core";
import { FrameView } from "./frames/FrameView";
import { usePaneTab } from "./useSession";

// StudioTab is the session pane's Studio (a lazy chunk): the session's IDE,
// framed (FrameView). SessionView keeps it mounted across tabs, so the IDE
// keeps its state; the pane closing ends it.
export default function StudioTab({ id }: { id: string }) {
  const q = usePaneTab(id, "studio");
  if (q.isPending) return <Loader size="sm" m="sm" />;
  if (!q.data?.studio) return <Text size="sm" c="dimmed" p="sm">The studio isn't available to you here.</Text>;
  return (
    <div id="studio" data-session={id} style={{ flex: 1, minHeight: 0, display: "flex", flexDirection: "column", height: "100%" }}>
      <FrameView sid={id} name="ide" kind="ide" open={q.data.ideOpen} reload />
    </div>
  );
}
