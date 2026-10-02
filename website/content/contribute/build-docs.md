---
title: Build the docs
linkTitle: Build the docs
weight: 30
description: Build and preview the Leoflow documentation site locally — Hugo + Docsy.
---

The documentation site is built with [Hugo](https://gohugo.io/) (extended) and the
[Docsy](https://www.docsy.dev/) theme, pulled in as a Hugo Module. Everything lives
under `website/`.

## Prerequisites

- **Hugo extended**, v0.110.0 or newer (`hugo version` must show `+extended`).
  CI pins `0.165.0`. Docsy needs the *extended* build — the plain build cannot
  compile the theme's SCSS.
- **Go** 1.26+ — used two ways: Hugo fetches Docsy (and its Bootstrap/Font Awesome
  deps) as **Hugo Modules**, and the CLI/Go reference generators run `go run`
  against the root module.
- **Node.js** 20+ — the PostCSS/autoprefixer toolchain the theme uses. Run
  `npm install` (or `npm ci`) inside `website/` once.
- **Python** 3.12+ — the reference generators post-process markdown and render the
  Python runtime API with `pdoc`. No system packages needed; `gen-python.sh`
  creates its own venv.

## First-time setup

```bash
cd website
npm ci                      # PostCSS toolchain (package-lock.json is committed)
hugo mod get                # fetch the Docsy module at the version pinned in go.mod
```

Do **not** run `hugo mod get -u` — that upgrades past the pinned Docsy version.
`hugo` auto-downloads modules at exactly the pinned versions on the next build.

## Generate the reference (do this before building)

Four sections of the site are **generated from source**. The rendered trees are
committed for preview convenience, but regenerate them whenever the source changes
(and CI reruns all four before every build, so they can never drift):

```bash
# run from the repo root
./website/scripts/gen-cli.sh       # CLI reference from Cobra   -> content/reference/cli/
./website/scripts/gen-go.sh        # Go packages from gomarkdoc -> content/reference/go/
./website/scripts/gen-openapi.sh   # OpenAPI spec for Scalar    -> static/openapi.yaml
./website/scripts/gen-python.sh    # Python runtime API (pdoc)  -> static/python-api/
```

Each script is idempotent and documents itself in a header comment. What they do:

| Script | Source | Output | Notes |
|---|---|---|---|
| `gen-cli.sh` | `go run ./cmd/dexaflow gen-docs` (Cobra) | `content/reference/cli/*.md` | Adds Hugo front matter; rewrites `.md` cross-links to pretty URLs. |
| `gen-go.sh` | `gomarkdoc` over a fixed package set | `content/reference/go/*.md` | Flattens one page per package; adds front matter. |
| `gen-openapi.sh` | `docs/api/openapi.yaml` | `static/openapi.yaml` | Read by `static/api-reference.html` (the embedded Scalar page). |
| `gen-python.sh` | `runtime/python` docstrings via `pdoc` | `static/python-api/` | A self-contained sidecar subsite, linked (not embedded) from `reference/python-api`. |

## Preview & build

```bash
cd website
hugo server                 # live-reload preview at http://localhost:1313/leoflow/
```

To produce the exact artifact CI publishes:

```bash
cd website
hugo --gc --minify          # output in website/public/
```

`--gc` also flags broken internal reference links, so a clean build means the
cross-references resolve. CI goes further: `website-build.yml` runs lychee offline
over the rendered `public/` tree and fails on any href or image src that does not
resolve, so that is the check to reproduce when a link report surprises you.

## How it is wired

- **`website/hugo.toml`** — site config: Docsy module import, the dev/latest version
  selector, the compact sidebar, and the top-navbar menu.
- **`website/layouts/`** — the two local overrides: `index.html` (the landing page)
  and `baseof.html` (a one-line Mermaid cache fix; see the comment in the file).
- **`website/content/`** — the docs tree, one directory per IA section.
- **`website/scripts/`** holds the four reference generators; `migration/` holds
  `build_redirects.py` and the one-time converters kept from the Hugo migration.
- **`.github/workflows/website-build.yml`** runs on every PR that touches
  `website/`: it installs the toolchains, runs the four generators, builds with
  `hugo --gc --minify`, then link-checks the rendered output with lychee. It
  uploads the build as an artifact and never deploys.
- **`.github/workflows/website-deploy.yml`** publishes to GitHub Pages on merge to
  `main`. It builds one leg per version (the latest GA at the root, `main` at
  `/dev/`, and a frozen tree per archived tag) and assembles them into a single
  Pages deploy. Which tag is "latest" and which are archived is data, in
  `website/scripts/ci/versions.json`.

## Redirects (old URLs keep working)

The MkDocs site this one replaced served flat `.html` URLs
(`use_directory_urls: false`), and the Hugo IA moved most pages into sections. To
keep bookmarks and external links alive, each moved page carries a **Hugo
`aliases:`** block in its front matter, so Hugo renders a redirecting stub at the
old path with no server config needed (it works on GitHub Pages under the
`/leoflow/` subpath).

That block is generated, not hand-maintained:

```bash
cd website
python3 scripts/migration/build_redirects.py
```

`build_redirects.py` reads `scripts/migration/link-map.csv` plus the generated
CLI/Go trees; the marked `AUTO redirect aliases` block it writes is safe to
regenerate. When you move or rename a page, update `link-map.csv` and rerun it.

{{% alert title="Rerun it after the reference generators" color="warning" %}}
`gen-cli.sh` and `gen-go.sh` rewrite each generated page's front matter from
scratch, which drops the alias block. Rerun `build_redirects.py` after either one.
CI runs the generators but not `build_redirects.py`, so the old `/cli/*.html` and
`/go/*.html` URLs do not currently redirect on the published site. That gap is
[#1122](https://github.com/dexadata/leoflow/issues/1122).
{{% /alert %}}
