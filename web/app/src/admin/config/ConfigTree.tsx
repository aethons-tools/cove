import { ActionIcon, Badge, Box, Group, Loader, NavLink, ScrollArea, TextInput } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { NavLink as RouterLink, useLocation } from "react-router";
import type { ApiError } from "../../api/client";
import { Problem } from "../../Problem";
import type { TreeNode, TreeResponse } from "../api/types";
import { ADMIN, adminApi, nodeHref } from "../paths";
import { ancestors, filterTree, loadOpen, saveOpen } from "./treeState";

// useTree is the Config tree, polled every 3 s while the page is visible
// (the old admin rail's rate).
export function useTree() {
  return useQuery<TreeResponse, ApiError>({ queryKey: ["admin", "tree"], queryFn: () => adminApi.get<TreeResponse>("tree"), refetchInterval: 3000 });
}

// ConfigTree is the Config pages' navigation: a node's label opens its page;
// a group's chevron opens or closes it. Open branches are remembered; the
// current page's branch is always open, and a filter opens everything.
export function ConfigTree() {
  const { data, error, isPending } = useTree();
  const { pathname, search } = useLocation();
  const [q, setQ] = useState("");
  const [open, setOpen] = useState(loadOpen);
  const current = pathname.startsWith(ADMIN) ? pathname.slice(ADMIN.length).replace(/\/$/, "") : "";
  const forced = new Set(ancestors(current));
  const filtering = q.trim() !== "";
  const toggle = (path: string) => {
    const next = new Set(open);
    if (next.has(path)) next.delete(path); else next.add(path);
    setOpen(next);
    saveOpen(next);
  };
  const rows = (nodes: TreeNode[], depth: number) => nodes.map((n) => {
    const expanded = filtering || open.has(n.path) || forced.has(n.path);
    const here = n.path === current;
    return (
      <Box key={n.path}>
        <Group gap={0} wrap="nowrap" pl={depth * 14}>
          {n.group
            ? <ActionIcon variant="subtle" size="sm" color="gray" aria-label={`${expanded ? "Collapse" : "Expand"} ${n.label}`} aria-expanded={expanded} onClick={() => toggle(n.path)}>{expanded ? "▾" : "▸"}</ActionIcon>
            : <Box w={22} />}
          <NavLink component={RouterLink} to={nodeHref(n.path)} end caseSensitive active={here} label={n.label} py={4}
            rightSection={n.badge > 0 ? <Badge size="sm" color={n.badgeRed ? "red" : "yellow"} aria-label={n.badgeTitle || `${n.badge}`}>{n.badge}</Badge> : null} />
        </Group>
        {n.group && expanded && rows(n.children, depth + 1)}
      </Box>
    );
  });
  return (
    <>
      <TextInput m="xs" size="xs" placeholder="Filter" aria-label="Filter the Config tree" value={q} onChange={(e) => setQ(e.currentTarget.value)} />
      {isPending ? <Loader m="md" size="sm" />
        : error ? <Problem error={error} here={pathname + search} />
        : <ScrollArea><Box component="nav" aria-label="Config" p="xs">{rows(filterTree(data.nodes, q), 0)}</Box></ScrollArea>}
    </>
  );
}
