import { useParams } from "react-router";
import { ConversationView } from "../conversation/ConversationView";

export function Conversation() {
  const { channel = "" } = useParams();
  return <ConversationView key={channel} id={channel} />;
}
