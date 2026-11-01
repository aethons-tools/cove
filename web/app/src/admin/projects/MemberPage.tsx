import { Group, Stack, Text } from "@mantine/core";
import { useNavigate, useParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import type { ProjectDetail, UserDetail } from "../api/types";
import { adminApi, nodeHref } from "../paths";
import { ConfirmAction } from "../ui/ConfirmAction";
import { Section } from "../ui/Section";
import { UserPage } from "../users/UserPage";
import { Delivery } from "./MembersPage";
import { ProjectGate } from "./ProjectPage";
import { useProjectWrite } from "./useProject";

const enc = encodeURIComponent;

// MemberPage is a member leaf: their membership here (delivery, Remove from
// the project) over the user's own page.
export function MemberPage() {
  return <ProjectGate body={(d) => <Stack><Membership d={d} /><UserPage /></Stack>} />;
}

function Membership({ d }: { d: ProjectDetail }) {
  const { user = "" } = useParams();
  const navigate = useNavigate();
  const p = d.name;
  const write = useProjectWrite(p);
  // The user's page below loads the same query: its name, for the not-a-member line.
  const { data: u } = useQuery<UserDetail>({ queryKey: ["admin", "user", user], queryFn: () => adminApi.get(`users/${enc(user)}`) });
  const m = d.members.find((x) => x.userId === user || x.name === user);
  if (!m) return u ? <Text size="sm" c="dimmed">{u.user.name} is not a member of {p}.</Text> : null;
  return (
    <Section title={`Member of ${p}`} sub="where they get DMs here; edit it on the project's Members page"
      actions={<ConfirmAction label={`Remove from ${p}`} confirm={`Remove ${m.name} from ${p}?`}
        onConfirm={async () => {
          await write.mutateAsync({ path: `members/${enc(m.userId)}`, del: true });
          void navigate(nodeHref(`config/projects/${enc(p)}/members`));
        }} />}>
      <Group><Delivery m={m} /></Group>
    </Section>
  );
}
