import { Button, Text, Textarea } from "@mantine/core";
import { useState } from "react";
import { MessageBody } from "../../conversation/MessageBody";
import type { OperatorNotes as Notes } from "../api/types";
import { useConfigEditable } from "../branch/branch";
import { adminApi } from "../paths";
import { EditForm } from "./EditForm";
import { Section } from "./Section";
import { useAdminMutation } from "./useAdminMutation";

// OperatorNotes is a config object's operator notes: markdown only operators
// see, never an agent. On a branch, path is their write (POST {notes}) and
// invalidate the page's query; with no path they live in a file (where:
// kit.yml, jam.yml) and are shown only. Nothing renders for no notes and
// nothing to edit.
export function OperatorNotes({ notes, path, where, invalidate, id }: {
  notes: Notes; path?: string; where?: string; invalidate?: unknown[][]; id?: string;
}) {
  const editable = useConfigEditable() && !!path;
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState(notes.markdown);
  const save = useAdminMutation((v: string) => adminApi.post<Notes>(path ?? "", { notes: v }), { invalidate, onSuccess: () => setEditing(false) });
  if (!notes.markdown && !editable) return null;
  const toggle = editable && (editing
    ? <Button size="xs" variant="subtle" onClick={() => setEditing(false)}>Cancel</Button>
    : <Button size="xs" variant="default" onClick={() => { setText(notes.markdown); setEditing(true); }}>{notes.markdown ? "Edit" : "Add notes"}</Button>);
  return (
    <div id={id}>
      <Section title="Operator notes" sub={`for operators only, never shown to agents${where ? ` · edit operator-notes in ${where}` : ""}`} actions={toggle}>
        {editing ? (
          <EditForm submit="Save notes" onSubmit={() => save.mutateAsync(text)}>
            <Textarea label="Notes" description="markdown" autosize minRows={4} maxRows={30} value={text}
              styles={{ input: { fontFamily: "var(--mantine-font-family-monospace)" } }} onChange={(e) => setText(e.currentTarget.value)} />
          </EditForm>
        ) : notes.html ? <MessageBody html={notes.html} plain={false} />
          : <Text c="dimmed" size="sm">No notes.</Text>}
      </Section>
    </div>
  );
}
