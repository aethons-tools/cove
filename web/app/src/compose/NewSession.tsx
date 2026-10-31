import { Button, Group, Modal, Radio, ScrollArea, Stack, Text, Textarea } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate } from "react-router";
import { apiGet, apiPost, type ApiError } from "../api/client";
import type { SessionRolesResponse, SessionStarted, StartSessionRequest } from "../api/types";
import { APP } from "../paths";

// NewSession starts a personal session: pick one of the roles the viewer may
// start (grouped by project; a full one is shown but can't be picked), add an
// optional first message, and land in the new session's conversation
// (onStarted: the caller's instead, as the VS Code rail does).
export function NewSession({ opened, onClose, onStarted }: { opened: boolean; onClose: () => void; onStarted?: (s: SessionStarted) => void }) {
  const [pick, setPick] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const go = useNavigate();
  const client = useQueryClient();
  const { data, error } = useQuery<SessionRolesResponse, ApiError>({ queryKey: ["session-roles"], queryFn: () => apiGet("session-roles"), enabled: opened });
  const roles = data?.roles ?? [];
  const projects = [...new Set(roles.map((r) => r.project))];
  const key = (project: string, role: string) => `${project}/${role}`;
  const chosen = roles.find((r) => key(r.project, r.role) === pick && !r.full);
  const close = () => { setPick(""); setMessage(""); onClose(); };
  const start = async () => {
    if (!chosen) return;
    setBusy(true);
    try {
      const req: StartSessionRequest = { project: chosen.project, role: chosen.role, message: message.trim() || undefined };
      const s = await apiPost<SessionStarted>("sessions", req);
      void client.invalidateQueries({ queryKey: ["rail"] });
      void client.invalidateQueries({ queryKey: ["session-roles"] });
      close();
      if (onStarted) onStarted(s);
      else void go(`${APP}c/${encodeURIComponent(s.channel)}`);
    } catch (e) {
      notifications.show({ color: "red", message: (e as ApiError).message });
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal opened={opened} onClose={close} title="New session">
      <form onSubmit={(e) => { e.preventDefault(); void start(); }}>
        <Stack>
          <Radio.Group label="Role" value={pick} onChange={setPick}>
            <ScrollArea.Autosize mah={280}>
              {projects.map((p) => (
                <Stack key={p} gap={4} mt="xs">
                  <Text size="xs" fw={700} c="dimmed" tt="uppercase">{p}</Text>
                  {roles.filter((r) => r.project === p).map((r) => (
                    <Radio key={key(r.project, r.role)} value={key(r.project, r.role)} disabled={!!r.full}
                      label={r.role} description={r.full ? `Full: ${r.full}` : undefined} />
                  ))}
                </Stack>
              ))}
            </ScrollArea.Autosize>
          </Radio.Group>
          {error && <Text size="sm" c="red">{error.message}</Text>}
          {data && roles.length === 0 && <Text size="sm" c="dimmed">No roles you can start a session of.</Text>}
          <Textarea label="First message" description="Optional: what the session should start on." autosize minRows={2} maxRows={8}
            value={message} onChange={(e) => setMessage(e.currentTarget.value)} />
          <Group justify="flex-end"><Button type="submit" disabled={!chosen} loading={busy}>Start</Button></Group>
        </Stack>
      </form>
    </Modal>
  );
}
