---
summary: The Draft section of a kit's page in the admin console, on a branch — opening and discarding a draft, the file tree, the text editor and its limits, upload, rename and delete, the diff against the base, publishing (a commit) and the unsaved-edit guard.
read_when: You are using or changing the Draft section of a kit's page in the admin console (`/app/admin/config/kits/<name>`), or a Draft form refused a save or a publish.
owns: the admin console's kit Draft section (on /app/admin/config/kits/<name>, `?dfile=`/`&ddiff=1`), its editor rules, its Published card, its ui kit draft log lines
prereqs: kits-drafts.md, config-branches.md (the Draft section shows only on a branch), ui-editing.md for the admin console's write rules
tier: leaf
updated: 2026-11-01
---

# The kit Draft section

A kit's page (`/app/admin/config/kits/<name>`, [ui-pages.md](ui-pages.md#kit-pages))
ends with its **Draft** section, on a branch only ([config-branches.md](config-branches.md)). With no draft open it offers **Open draft**
from any version (current preselected). The opened file is `?dfile=<path>`
in the page's URL (its diff against the base `&ddiff=1`). The Kits table and
the page head flag a kit whose draft is open (`draft · base v<N>`, which
jumps to the section). Every write follows the console's
[write rules](ui-editing.md) and sends the draft's ETag back, so the
[concurrency](kits-drafts.md#concurrency) rule holds in the browser too.

- **Draft** card: base and current versions, the last save's operator and
  time, **Discard** (it asks first), and a notice when a newer version has
  landed since the draft was opened.
- **Publish** card: the problems, if any (the button stays disabled, and a
  forged request is still refused 400), and the **Pin followers** box (leave
  it off on a branch: [kits-drafts.md](kits-drafts.md#publish)); **Publish**
  asks first, and commits the kit's files to the branch.
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
- A save that lost a race (another tab saved first) is refused
  with your text still in the editor, unsaved, and **Reload** to re-read the
  draft.
- While the editor holds unsaved text, any other Draft action or a
  navigation asks first (`You have unsaved changes in the editor. Discard
  them?`), and leaving the page gets the browser's own prompt: each would drop
  it.

The **Published** card shows the new version (linked), each follower
pinned and any warning.

UI writes log `ui kit draft opened|saved|discarded|published` with the
kit, the operator and the tree's first 12 hex digits (a publish also the
version, base and count pinned), never content or a path.

