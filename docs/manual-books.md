---
summary: The team site's books (Antora components on "Jam books" — the Jam manual and the process book from the private aethons-tools/jam-config, pulled in by manual/extensions/books.js from a local clone or CI checkout with the JAM_CONFIG_DEPLOY_KEY secret) and its full-text search (@antora/lunr-extension) with the UI contract it needs.
read_when: You are adding a book to the team site, building the team site with or without the process book, setting up its jam-config deploy key, or touching search or the book switcher.
owns: the team site's books (the content sources and manual/extensions/books.js), the process book's branch-to-version mapping and how it is pulled in (JAM_CONFIG_DIR, JAM_CONFIG_DEPLOY_KEY), and search (@antora/lunr-extension and its side of manual/ui)
prereqs: manual.md for the manual, its versions and its builds
tier: leaf
updated: 2026-11-11
---

# Books and search on the team site

- **Books.** The team site, titled *Jam books* (`site.title`; the customer
  manual stays *Jam manual*), hosts several books, each an Antora component:
  this manual is `jam`. The header lists every component (`site.components`),
  the current one highlighted, beside the version switcher; with one book the
  header has no book list (the site title names it). To add a book, add a content source to
  `antora-playbook.yml` (and `antora-playbook.local.yml` if it should build
  locally): a repo `url`, `start_path` to the dir holding its `antora.yml`
  (its own `name`, `title`, `nav`), and `branches` — or, for a repo not
  everyone can read, an entry in the `books` list (below). Its pages use our UI and
  extensions; `internal` applies to it as to `jam`. Never add one to
  `antora-playbook.jam.yml`: the customer manual is `jam` alone
  (`scripts/manual.sh` and the extension tests check it).
- **The process book** (`process`, *Process*: how we run Jam's development)
  lives in the private `aethons-tools/jam-config` under `process/`; its
  `main` is version `current` (the default) and `goal` is `goal`. A missing
  branch, or one without `process/`, is skipped. Antora's git can't do SSH and
  a playbook can't read the environment, so it is not a content source: the
  `books` list of `manual/extensions/books.js` in `antora-playbook.yml` adds it
  from a local clone — `$JAM_CONFIG_DIR` (which must then exist), else
  `../jam-config` beside your cove checkout, else none (a warning, and the site
  is the manual alone). It reads the local branch of each name, else origin's,
  like cove's; fetch first. CI checks jam-config out into the workspace with
  the read-only deploy key in secret `JAM_CONFIG_DEPLOY_KEY` (not kept for later
  steps) and sets `JAM_CONFIG_DIR`; without the secret it warns and deploys the
  manual alone. Tests: `manual/extensions/test/books.test.js`.
- **Search** is `@antora/lunr-extension` (pinned in `manual/package.json`),
  enabled in the team and local playbooks only: the customer manual is
  embedded in Jam, whose CSP allows no scripts, and `scripts/manual.sh` fails
  if a search index ends up in it. It writes `search-index.js` at the site
  root (all books and versions, internal content included — the site is
  Access-gated; about 50 KB today). The UI's side of the contract, in
  `ui/layouts/default.hbs`, all under `{{#if env.SITE_SEARCH_PROVIDER}}`:
  `<input id="search-input" type="text">` inside `#search-field` (the results
  dropdown attaches to it), an "In this book" checkbox
  (`data-facet-filter="component:<name>"`) when there are several books, and
  `{{> search-scripts}}` before `</body>` (the extension supplies that partial,
  `lunr.js`, `search-ui.js` and `search.css`; the UI must not ship them). The
  indexer reads only `article.doc` and takes the title from its `h1.page`, so
  keep page content in them.
