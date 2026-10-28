import { Anchor, Card, Group, Loader, Stack, Text, TextInput, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useLocation, useNavigate } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { Created, ProjectsResponse } from "../api/types";
import { adminApi, nodeHref } from "../paths";
import { DataTable } from "../ui/DataTable";
import { EditForm } from "../ui/EditForm";
import { ManagedBadge } from "../ui/ManagedBadge";
import { useAdminMutation } from "../ui/useAdminMutation";
import { projectsKey } from "./useProject";

// ProjectsPage is Config › Projects: every project, and New project.
export function ProjectsPage() {
  const { data, error, isPending } = useQuery<ProjectsResponse, ApiError>({ queryKey: projectsKey, queryFn: () => adminApi.get("projects") });
  const here = useLocation();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const create = useAdminMutation((n: string) => adminApi.post<Created>("projects", { name: n }), {
    invalidate: [projectsKey], onSuccess: (c) => void navigate(nodeHref(c.path)),
  });
  return (
    <Stack>
      <div>
        <Title order={2}>Projects</Title>
        <Text size="sm" c="dimmed">Each project's members, agents, rooms and escalation — open one in the tree or below</Text>
      </div>
      <details>
        <summary style={{ cursor: "pointer" }}>New project</summary>
        <Card withBorder mt="xs">
          <EditForm submit="Create project" onSubmit={() => create.mutateAsync(name)}>
            <TextInput label="Name" required value={name} onChange={(e) => setName(e.currentTarget.value)} />
          </EditForm>
        </Card>
      </details>
      {isPending ? <Loader size="sm" /> : error ? <Problem error={error} here={here.pathname} /> : (
        <DataTable rows={data.projects} rowKey={(p) => p.name} empty="No projects." columns={[
          { key: "n", label: "Name", render: (p) => <Group gap="xs"><Anchor component={Link} to={nodeHref("config/projects/" + encodeURIComponent(p.name))} fw={600}>{p.name}</Anchor><ManagedBadge managed={p.managed} /></Group> },
          { key: "m", label: "Members", render: (p) => p.members },
          { key: "r", label: "Roles", render: (p) => p.roles },
          { key: "o", label: "Rooms", render: (p) => p.rooms },
        ]} />
      )}
    </Stack>
  );
}
