---
summary: The admin UI's Draft tab on a kit page — opening and discarding a draft, the file tree, the text editor and its limits, upload, rename and delete, the diff against the base, publishing (the config-managed confirmation and the config-repo panel) and the unsaved-edit guard.
read_when: You are using or changing the kit page's Draft tab in the admin UI (`/app/admin/kits/<name>/draft`), or a Draft-tab form refused a save or a publish.
owns: the admin UI Draft tab (/app/admin/kits/<name>/draft), its editor rules, its config-managed publish confirmation and config-repo panel, its ui kit draft log lines
prereqs: kits-drafts.md, ui-editing.md for the admin UI's write rules
tier: leaf
updated: 2026-10-25
---

# The kit Draft tab

A kit's page (`/app/admin/kits/<name>`) has, while no draft is open, an
**Open draft from v<N>** button for the version on view, and a **Draft** tab
(`/app/admin/kits/<name>/draft`) whose form opens one from any version
(current preselected). The Kits table and the page head flag a kit whose
draft is open. Every write is a form under the admin UI's
[write rules](ui-editing.md) and sends the draft's ETag back from a hidden
field, so the [concurrency](kits-drafts.md#concurrency) rule holds in the browser too.

- **Draft** card: base and current versions, the last save's operator and
  time, **Discard** (it asks first), and a notice when a newer version has
  landed since the draft was opened.
- **Publish** card: the problems, if any (the button stays disabled, and a
  forged POST is still refused 400), the **Pin followers** box and, for a
  config-managed kit, the drift warning.
- **Files**: the draft's tree by directory, with each file's mode, size and
  its change against the base; files dropped since the base are listed last,
  struck through. **Add file** takes a path and either an upload or nothing
  (an empty text file).
- **Changes** diffs each file against the base, and `kit.yml` gets its own
  line diff, with a packed context masked on both sides.
- A file opens in a plain text editor when it is UTF-8 text of 1 MiB or
  less with no carriage returns; otherwise it shows a note, **Download** and
  a mode toggle. A `kit.yml` holding a packed context is never shown: the
  note says to download it, remove the context, and replace it. Any file can
  be replaced by an upload (the upload's own file name is never used),
  renamed or deleted.
- A save that lost a race answers with the editor still holding your text,
  unsaved, Save disabled, and a reload link.
- While the editor holds unsaved text, any other Draft-tab action, a link
  or leaving the page asks first (`You have unsaved changes in the editor.
  Discard them?`): each would re-render the tab without it.

Publishing a config-managed kit from the UI is allowed, with a step the
API does not have: the form shows the drift warning and needs the box
**I understand this drifts from the config repo** ticked (the server
refuses 400 `kit <name> is managed by the config repo: tick “…” to
publish` without it). When **Pin followers** would rewrite config-managed
followers, the form names them next to the box, and with Pin ticked the
same confirmation is needed (400 `pinning followers drifts config-managed
<kits> from the config repo: tick “…” to publish`). The box is checked
before the draft's existence and ETag, so an unticked publish of a gone or
stale draft answers this 400, not 404 or 412. The spec's "write to config repo instead" is read
as *publish, then write to the config repo*: after any publish that drifts
(the kit, or a managed follower it pinned), the result card lists the
config-repo files to expect — `kits/<name>/<path>` for each file that
changed, `kits/<follower>/kit.yml` per managed follower pinned, paths and
change kinds only — and says to run `at-jam config write <repo>` in the
checkout, check `git diff`, and commit. Every publish's result card shows
the new version, each follower pinned and any warning.

UI writes log `ui kit draft opened|saved|discarded|published`, shaped
like the API's [audit lines](kits-drafts.md#api) and likewise never naming content or a
path.

