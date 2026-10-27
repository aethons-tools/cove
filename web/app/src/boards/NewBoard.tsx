import { Button, Group, Modal, Stack, TextInput } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useState } from "react";
import { useNavigate } from "react-router";
import { apiPost, type ApiError } from "../api/client";
import type { NewBoardResponse } from "../api/types";

// NewBoard is New whiteboard: a title, then the new board, opened.
export function NewBoard({ channel }: { channel: string }) {
  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState("");
  const [busy, setBusy] = useState(false);
  const go = useNavigate();
  const start = async () => {
    setBusy(true);
    try {
      const b = await apiPost<NewBoardResponse>("boards", { channel, title: title.trim() });
      setOpen(false);
      void go(`/app/next/boards/${encodeURIComponent(b.board)}`);
    } catch (e) {
      notifications.show({ color: "red", message: (e as ApiError).message });
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <Button size="xs" variant="default" onClick={() => { setTitle(""); setOpen(true); }}>New whiteboard</Button>
      <Modal opened={open} onClose={() => setOpen(false)} title="New whiteboard" size="sm">
        <form onSubmit={(e) => { e.preventDefault(); if (title.trim()) void start(); }}>
          <Stack>
            <TextInput label="Title" value={title} onChange={(e) => setTitle(e.currentTarget.value)} data-autofocus />
            <Group justify="flex-end"><Button type="submit" disabled={!title.trim()} loading={busy}>Start</Button></Group>
          </Stack>
        </form>
      </Modal>
    </>
  );
}
