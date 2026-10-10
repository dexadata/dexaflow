#!/usr/bin/env bash
# Generate the CLI reference for the Hugo site from Cobra.
#
# Parity with the live MkDocs pipeline (.github/workflows/docs.yml), which runs
#   go run ./cmd/dexaflow gen-docs --dir docs/cli
# Here the same generator writes into website/content/reference/cli/ and the raw
# Cobra markdown is post-processed for Hugo:
#   * a front-matter block (title / linkTitle / weight) is prepended, since
#     Cobra emits a bare "## <command>" heading with no front matter and Docsy
#     needs one to title and order the page;
#   * the "SEE ALSO" cross-links (`](dexaflow_foo.md)`) are rewritten to the
#     Hugo pretty URL (`](/reference/cli/dexaflow_foo/)`) — with Goldmark's link
#     render hooks disabled (see hugo.toml), a bare `.md` link would 404.
#
# Idempotent: reruns overwrite the generated *.md and never touch _index.md.
# The generated tree IS committed (parallel-track preview convenience); CI
# reruns this before `hugo` so the pages can never drift from the binary.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT_DIR="${REPO_ROOT}/website/content/reference/cli"

cd "${REPO_ROOT}"
echo "gen-cli: generating Cobra markdown into ${OUT_DIR}"
go run ./cmd/dexaflow gen-docs --dir "${OUT_DIR}"

echo "gen-cli: post-processing for Hugo (front matter + link rewrite)"
python3 - "${OUT_DIR}" <<'PY'
import os, re, sys

out_dir = sys.argv[1]
# Every generated file is named after its command path (dexaflow, dexaflow_lite,
# dexaflow_lite_backup, ...). Sorted, "dexaflow.md" (the root overview) sorts first.
files = sorted(f for f in os.listdir(out_dir)
               if f.endswith(".md") and f != "_index.md")

for weight, name in enumerate(files, start=1):
    path = os.path.join(out_dir, name)
    with open(path, encoding="utf-8") as fh:
        body = fh.read()

    # First "## <command>" line is the command name; use it as the title and
    # drop it (Docsy renders the front-matter title as the page <h1>, so the
    # heading would otherwise duplicate it).
    m = re.search(r"^## (.+)$", body, re.MULTILINE)
    title = m.group(1).strip() if m else name[:-3].replace("_", " ")
    if m:
        body = body[:m.start()] + body[m.end():]
    body = body.lstrip("\n")

    # Shorter sidebar label: drop the leading "dexaflow " (keep just "dexaflow"
    # for the root page).
    link_title = title[len("dexaflow "):] if title.startswith("dexaflow ") else title

    # Rewrite sibling ".md" links to Hugo pretty URLs. canonifyURLs (hugo.toml)
    # then prefixes the site's base URL uniformly.
    body = re.sub(r"\]\((dexaflow[\w.-]*)\.md\)",
                  lambda mm: "](/reference/cli/%s/)" % mm.group(1),
                  body)

    # Before the rename every page was leoflow_<path>; keep those URLs (and the
    # older MkDocs ones) alive as redirects to the dexaflow_<path> page.
    stem = name[:-3]
    legacy = "leoflow" + stem[len("dexaflow"):] if stem.startswith("dexaflow") else ""
    aliases = ""
    if legacy:
        aliases = ("aliases:\n"
                   f"  - /cli/{legacy}.html\n"
                   f"  - /reference/cli/{legacy}/\n")
    fm = (
        "---\n"
        f"{aliases}"
        f"title: \"{title}\"\n"
        f"linkTitle: \"{link_title}\"\n"
        f"weight: {weight}\n"
        "---\n\n"
    )
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(fm + body)

print(f"gen-cli: wrote front matter to {len(files)} page(s)")
PY

echo "gen-cli: done"
