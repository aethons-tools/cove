-- Whiteboards (intercom whiteboards): a board in a channel, its elements one
-- row each, merged per element; boards.seq stamps accepted changes so a
-- delta read (seq > since) never misses one. Element docs are stored as
-- text, exactly as sent.
CREATE TABLE boards (
    id         text PRIMARY KEY,
    channel_id text NOT NULL,
    title      text NOT NULL,
    created_by text NOT NULL,
    created_at timestamptz NOT NULL,
    seq        bigint NOT NULL DEFAULT 0
);
CREATE INDEX boards_channel ON boards (channel_id);

CREATE TABLE board_elements (
    board_id      text NOT NULL REFERENCES boards (id),
    element_id    text NOT NULL,
    version       bigint NOT NULL,
    version_nonce bigint NOT NULL,
    deleted       boolean NOT NULL,
    seq           bigint NOT NULL,
    doc           text NOT NULL, -- verbatim: jsonb would rewrite numbers (1e99999 expands)
    PRIMARY KEY (board_id, element_id)
);
CREATE INDEX board_elements_seq ON board_elements (board_id, seq);
