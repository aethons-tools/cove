-- Session colors (docs/usage/jam/session-colors.md): each session id's color,
-- assigned when it is first raised. Kept after the session ends (a standing
-- session's re-raise and an ended session's messages keep it); never deleted.
CREATE TABLE session_colors (
    actor_id text PRIMARY KEY,
    color    text NOT NULL
);
