-- content_type: how body is meant to be read (MIME). Existing rows predate the
-- field and read as the default, markdown.
ALTER TABLE squawks ADD COLUMN content_type text NOT NULL DEFAULT 'text/markdown';
