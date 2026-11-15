-- Conversation kinds (docs/modules/ROOT/pages/usage/jam/conversations.adoc#kinds):
-- a chat becomes a dm (two members) or a group (three or more), counted from
-- its key, the comma-joined member ids; a ticket channel becomes its unit's
-- work log. The kind is in both the column and the doc.
UPDATE channels
SET kind = CASE WHEN key LIKE '%,%,%' THEN 'group' ELSE 'dm' END
WHERE kind = 'chat';
UPDATE channels SET kind = 'work' WHERE kind = 'ticket';
UPDATE channels SET doc = jsonb_set(doc, '{kind}', to_jsonb(kind)) WHERE doc->>'kind' IS DISTINCT FROM kind;

-- A membership's wake mode: named (only messages that name the member wake
-- it) or all. Stored; waking does not read it yet.
ALTER TABLE channel_members ADD COLUMN wake text NOT NULL DEFAULT 'named' CHECK (wake IN ('named', 'all'));
