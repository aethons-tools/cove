import { Anchor, Text } from "@mantine/core";
import { useParams } from "react-router";

// Conversation is a placeholder until slice 2 ports the conversation pane.
export function Conversation() {
  const { channel = "" } = useParams();
  return (
    <Text>
      This preview doesn't show conversations yet.{" "}
      <Anchor href={`/app/user/?c=${encodeURIComponent(channel)}`}>Open in the current Inbox</Anchor>
    </Text>
  );
}
