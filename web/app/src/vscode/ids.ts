// The one app-side copy of the id shapes the bridge and links accept.
// SES_RE is internal/ident's session id; STREAM_RE is sessionevents.ValidStreamID.
export const SES_RE = /^ses_[0-7][0-9a-hjkmnp-tv-z]{25}$/;
export const STREAM_RE = /^[0-9a-f]{32}$/;
