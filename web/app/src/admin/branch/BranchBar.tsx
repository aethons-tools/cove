import { Alert, Anchor, Badge, Button, Group, Menu, Modal, Paper, Stack, Text, TextInput } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router";
import { ApiError } from "../../api/client";
import type { ConfigBranchResult, ConfigBranches, ConfigMergeResult, ConfigPR } from "../api/types";
import { ADMIN, adminApi } from "../paths";
import { ConfirmAction } from "../ui/ConfirmAction";
import { setBranch, useBranch, useHead } from "./branch";

const listKey = ["admin", "config", "branches"];

// BranchBar heads every Config page (docs/usage/jam/config-branches.md):
// the followed branch, read-only, with Create and Open branch; or the open
// branch, with its links, Merge, PR and Back. Without a config repo (404)
// the config is read-only, and it says so; another failure is shown.
export function BranchBar() {
  const branch = useBranch();
  const head = useHead();
  const client = useQueryClient();
  const navigate = useNavigate();
  const { data, error } = useQuery<ConfigBranches, ApiError>({ queryKey: listKey, queryFn: () => adminApi.get("config/branches"), retry: false });
  const pr = useQuery<ConfigPR, ApiError>({ queryKey: [...listKey, "pr", branch], queryFn: () => adminApi.get(`config/branches/pr?branch=${encodeURIComponent(branch)}`),
    enabled: !!branch && !!data?.canPR, retry: false });
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [refused, setRefused] = useState("");
  const [prURL, setPrURL] = useState("");
  const noRepo = error?.status === 404;
  // A tab still holding a branch (sessionStorage) on a Jam with no repo would
  // render forms whose writes always 409: drop it, so the console is read-only.
  useEffect(() => {
    if (noRepo && branch) {
      setBranch("");
      void client.invalidateQueries();
    }
  }, [noRepo, branch, client]);
  // No config repo: no branches, so nothing here edits the config. Any
  // other failure says why, rather than hiding the bar.
  if (noRepo) {
    return <Alert color="gray" mb="md" title="Read-only">This Jam has no config repo. Set config.github, config.gitlab or config.git in the serve config to edit its config on branches.</Alert>;
  }
  if (error) {
    return <Alert color="red" mb="md" title="The config branches can't be listed">{error.message}</Alert>;
  }
  if (!data) return null;
  // switch changes the context: every admin query reloads, from the config root.
  const switchTo = (b: string) => {
    setBranch(b);
    setRefused("");
    setPrURL("");
    void client.invalidateQueries();
    navigate(ADMIN + "config");
  };
  const current = data.branches.find((b) => b.name === branch);
  const create = async () => {
    const r = await adminApi.post<ConfigBranchResult>("config/branches", { name: name.trim() });
    setCreating(false);
    notifications.show({ color: "green", message: r.message });
    switchTo(r.branch.name);
  };
  const merge = async () => {
    try {
      const r = await adminApi.post<ConfigMergeResult>("config/branches/merge", { branch, head: head || current?.head || "" });
      notifications.show({ color: "green", message: r.message });
      setRefused("");
      void client.invalidateQueries();
    } catch (e) {
      if (!(e instanceof ApiError) || e.status !== 409) throw e;
      setRefused(e.message);
      void client.invalidateQueries({ queryKey: listKey }); // a stale head: read it again
    }
  };
  const openPR = async () => {
    const r = await adminApi.post<ConfigPR>("config/branches/pr", { branch });
    setPrURL(r.url);
    void client.invalidateQueries({ queryKey: [...listKey, "pr", branch] });
  };
  const link = prURL || pr.data?.url || "";
  const noun = data.prNoun || "pull request";
  const others = data.branches.filter((b) => !b.followed);
  const openMenu = others.length > 0 && (
    <Menu>
      <Menu.Target><Button size="xs" variant="light">Open branch</Button></Menu.Target>
      <Menu.Dropdown>
        {others.map((b) => <Menu.Item key={b.name} onClick={() => switchTo(b.name)}>{b.name} <Text span c="dimmed" ff="monospace">@{b.short}</Text></Menu.Item>)}
      </Menu.Dropdown>
    </Menu>
  );
  return (
    <Paper withBorder p="xs" mb="md">
      {!branch ? (
        <Group justify="space-between">
          <Text size="sm">Viewing <b>{data.followed}</b>: what's running (read-only). Edit on a branch, then Merge or open a PR.</Text>
          <Group gap="xs">
            <Button size="xs" onClick={() => { setName(data.defaultName); setCreating(true); }}>Create branch</Button>
            {openMenu}
          </Group>
        </Group>
      ) : (
        <Stack gap="xs">
          <Group justify="space-between">
            <Group gap="xs">
              <Badge color="yellow">branch</Badge>
              <Text size="sm">Editing <b>{branch}</b> {(head || current) && <Text span ff="monospace" c="dimmed">@{head ? head.slice(0, 7) : current?.short}</Text>}: each save is a commit to it.</Text>
              {data.treeLink && <Anchor size="sm" href={data.treeLink + branch} rel="noopener noreferrer">View the branch</Anchor>}
              {link && <Anchor size="sm" href={link} rel="noopener noreferrer">{noun[0]!.toUpperCase() + noun.slice(1)}</Anchor>}
            </Group>
            <Group gap="xs">
              <ConfirmAction live label="Merge" color="jam" variant="filled" confirm={`Merge ${branch} into ${data.followed}? The Jam applies it at once.`} onConfirm={merge} />
              {data.canPR && !link && <Button size="xs" variant="light" onClick={() => void openPR()}>{data.prNoun === "merge request" ? "MR" : "PR"}</Button>}
              {openMenu}
              <Button size="xs" variant="subtle" onClick={() => switchTo("")}>Back to {data.followed}</Button>
            </Group>
          </Group>
          {refused && (
            <Alert color="red" title="Not merged">
              <Stack gap="xs">
                <Text size="sm">{refused}</Text>
                {data.canPR && !link && <Group><Button size="xs" onClick={() => void openPR()}>Open a {noun}</Button></Group>}
              </Stack>
            </Alert>
          )}
        </Stack>
      )}
      <Modal opened={creating} onClose={() => setCreating(false)} title="Create branch">
        <Stack>
          <TextInput label="Branch name" description={`Starts at ${data.followed}'s head.`} value={name} onChange={(e) => setName(e.currentTarget.value)} data-autofocus />
          <Group justify="flex-end"><Button onClick={() => void create().catch((e: Error) => notifications.show({ color: "red", message: e.message }))} disabled={!name.trim()}>Create</Button></Group>
        </Stack>
      </Modal>
    </Paper>
  );
}
