-- The legacy log's /me unread cursors (keyed by synthetic channel names):
-- unused since the channel log's read cursors (0011, channel_reads). The
-- legacy log's History reads as all read.
DROP TABLE IF EXISTS intercom_unread_cursors;
