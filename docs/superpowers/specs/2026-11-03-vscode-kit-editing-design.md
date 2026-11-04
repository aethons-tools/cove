# Jam in VS Code: editing kits

Status: approved (2026-11-04). Builds on [the hub design](2026-10-30-vscode-hub-design.md) and the [config branch editor](2026-10-31-config-branch-editor-design.md).

## Why

The console's kit editor is clunky. A kit is a folder of files (kit.yml, a Dockerfile, scripts, note files), and the console makes you work on them one file at a time in a bespoke editor. An IDE already does this well: a file tree, real editors, search, multi-file edits, diffs. So kits should be edited in VS Code, as an ordinary folder, with the files **served by the Jam**. The user chose this over a local clone of the config repo: "the best experience, even if it is more work".

Before an edit counts, the user wants to **at least verify that it builds**. Bringing a session up on the edited kit "might be nice", so it is a later step.

This is the first piece of a bigger move. The user wants the console's config editing "all in VS Code at some point", and that is why the extension gets **full** operator config access now, not a kit-only slice.

## Goals

- From the hub, open any kit on your config branch as a folder in VS Code and edit its files with the normal tools. A save in VS Code writes the Jam's draft of that kit.
- **Build** a kit's draft from VS Code, with the live build output and the result (success or failure) visible in VS Code. The draft's problems (as the Jam lists them today) show in the Problems view.
- **Publish** a draft (commit it to your branch) and **Discard** it from VS Code.
- See and switch which config branch you are working on.
- Operator app tokens can use the whole admin API, so later slices can move more of the console into VS Code without touching access again.

## Non-goals

- **Try in a session** (raise a studio on the draft). It is the next step, with its own design. This one must not make it harder: build images and keys must be usable by a later draft raise.
- Other config objects (roles, users, destinations…) in VS Code. Access is opened up for them, but no UI is built.
- Merging a branch or opening a PR from VS Code. *(My call:)* a link opens the branch in the console, which does both today.
- Kits that live in their own git repo (`kits/<n>.source.yml`). They are edited in their repo. *(My call:)* they are listed read-only, or not at all.
- Creating, renaming or deleting whole kits. *(My call:)* the console still does that; it is cheap to add later.
- Removing the console's kit editor. It stays, on the same drafts.

## Anti-goals (failure even if it "works")

- Losing an edit without being told. A save that the Jam refused must not look saved.
- Two editors on the same kit silently overwriting each other (VS Code and the console, or two VS Code windows).
- A member (non-operator) token, or a token minted before this change, gaining admin access.
- The extension's admin access being wider than the same operator's browser session, except for the standing lifetime accepted below.

## Decided

- **Files come from the Jam.** Not a local git clone.
- **One model for console and VS Code: the draft.** *(My call, playing back the conversation's model:)* each kit on a branch has one draft, the working copy the console already keeps (`docs/usage/jam/kits-drafts.md`). VS Code opens that draft. A VS Code save is a draft save, not a commit. **Publish** commits. Editing the same kit in the console and VS Code means working on the same draft, guarded by its ETag.
- **Full access for operator tokens.** An app token with operator standing may use the whole admin API (`/app/admin/api/`), exactly as the operator's browser session can, including the branch headers and the 409s off a branch.
- **Build is a check, not a gate.** A good build commits nothing; you still choose when to publish. Publish does not require a build.

## How it works

### Access

Today `/app/admin/` refuses every bearer ([app-tokens.md](../../usage/jam/app-tokens.md)).

- `/app/admin/api/` accepts a bearer whose token has operator standing **and** was minted with admin consent. The HTML console stays bearer-free.
- *(My call:)* admin consent is new and explicit, but not a choice. When the signing-in user is an operator, the consent page says the client gets admin access, and the token records an admin flag. There is no opt-out checkbox and no new client parameter. Tokens minted before this change carry no flag and stay refused, so access never widens silently. The exchange answer says whether the token has admin access. The extension shows the Kits view only then, and it asks for a new sign-in when an operator's old token gets the "no admin access" refusal.
- The rules a bearer has elsewhere apply: a cookie plus a bearer is a 400; revocation and the 30-day fixed lifetime of operator tokens are unchanged.
- **Standing is not re-checked (the user's choice, "fine for now").** A token keeps the operator standing recorded at consent for its 30 days, unlike the browser session, which drops it soon after the IdP removes `jam:admin`. Demoting an operator therefore also means revoking their app tokens; the docs say so. A shorter admin window or a re-check is a possible later change. The operator is the token's recorded operator (the branch router already falls back to `Principal.Operator.ID` for commit authorship).
- The extension's host proxy accepts admin API paths only for its own host-side calls (the kit filesystem, the kits view). *(My call:)* webview panes stay limited to `/app/user/` until a slice puts an admin pane in VS Code.

### Branch

- The Kits view shows the branch you are on. *(My call:)* default is your default branch `jam/<operator>`; if it does not exist yet, the view offers **Create branch**. **Switch branch** lists the branches and can create one, as the console does.
- The chosen branch is remembered per Jam. Every admin call sends `X-Jam-Config-Branch` (and the head where the console does). A stale-head refusal reloads, it does not retry blindly.
- **Open in console** opens the branch in the browser for merge and PR.

### Kits view and the kit folder

- A **Kits** view in the hub window's Jam sidebar, for operators only, lists the kits on the branch, marking those with an open draft.
- Opening a kit opens its draft (opening one from the current version if none is open, as the console's **Open draft** does) and shows its files as a folder in VS Code, through a `jamkit:` file system the extension registers. *(Builder's call:)* whether the folder joins the hub's workspace or opens in its own window, as long as the explorer, search and the editors work on it and you can tell which Jam, branch and kit it is.
- File operations map onto the draft API: read, save, add, rename, delete. Executable files must be settable from VS Code (*builder's call how:* a command, a context-menu entry).
- **Concurrency.** *(My call:)* the draft's ETag covers the whole tree, so any change anywhere would refuse every other file's save. Instead, a file save is conditional on **that file**: the save names the content (hash) VS Code last read for it. If the file on the Jam still has that content, the save goes through even when other files changed. If it doesn't, the save fails as a visible VS Code save error, the folder refreshes, and VS Code's normal "file changed on disk" compare resolves it. Nothing is overwritten. Tree changes (add, rename, delete) are conditional on the paths they touch in the same way. The server gains this per-file precondition; the console keeps its whole-draft ETag.
- *(My call:)* draft saves do not send the branch head. Edits elsewhere on the branch (another kit's publish, a role) don't refuse draft saves. Publish does send it, as the console does.
- **Draft gone.** If the draft vanished (discarded elsewhere, or dropped because the kit changed on the branch), saves fail visibly and the view says so and offers to reopen. Unsaved editor buffers are untouched.
- Text and binary files both read; files over the draft's size limit are refused on save with the Jam's message.

### Problems, Build, Publish, Discard

- The draft's **problems** go to VS Code's Problems view, on the file they name (on kit.yml when they name none), and update after every save.
- **Build** (a command and a button on the kit) builds the draft's image on a studio manager. The log streams into an Output channel, and the end is a clear success or failure notification. **Cancel** stops it. Building again an unchanged draft reuses the result.
- **Publish** commits the draft to the branch, refused with the Jam's message when there are problems or no changes. **Discard** asks first.

### Server: reviving draft builds

The test-build machinery still exists (`internal/jam/kit_draft_build.go`, `launcher/draft.go`, the manager wire op `build-kit-draft`, wired in `cmd/jam/main.go`). Since #597 it has no route, and it reads the live store, while drafts now live in branch workspaces.

- Builds resolve the draft and its parents against the **branch's workspace**, and the job key includes the branch.
- Routes under the kit's draft path on the admin API: start a build, read its status and log from an offset, cancel. They never commit (the branch router must not save after them).
- *(My call:)* a draft image's tag names the kit and the digest of everything it is built from (the draft's tree **and** its resolved parents), so the same draft on two branches with different parents can't share a tag, and two builds of identical inputs share one. Builds of the same tag are serialized. The build record keeps the branch and that digest, so a later draft raise can find and reuse the image.
- Without a draft-capable studio manager, Build answers clearly (the existing 503), and VS Code shows it.
- The docs' "no test build" statements are replaced ([kits-drafts.md](../../usage/jam/kits-drafts.md) and its `#test-build` anchor).

## Constraints

- Drafts are held in memory in the branch workspace. A Jam restart loses them; an edit saved to a draft but not published is gone. *(My call:)* this design does not change that, but VS Code must show it (the "draft gone" case above), and the docs say it. Making drafts durable is a candidate follow-up.
- The draft API's shapes were made for the console's pages (`DraftPage`, the editor's `text`). The builder may add plain endpoints (tree, raw read) if mapping the file system onto the existing ones is awkward.
- The guard test `TestNoRaiseSetsKitDraft` stays: builds do not raise studios.
- Desktop VS Code and VSCodium, macOS first, as for the rest of the extension.

## Left to the builder

- Slicing (a likely order: access → branch and kits view → kit file system → problems → build → publish and discard).
- The exact routes, payloads and extension commands, keeping the console's names where they exist.
- How the Output channel and notifications are laid out.
