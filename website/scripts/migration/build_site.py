#!/usr/bin/env python3
"""
Orchestrate the docs/ -> website/content/ migration (bulk copy+convert set).

Handles the mechanical 1:1 pages: PAGES (iamap), the 58 ADRs, and the 54
connection recipes. Emits:
  * website/content/**              migrated pages
  * spec/out/link-map.csv           old source path -> new Hugo URL (feeds F4 redirects)
  * spec/out/unmapped-links.csv     doc links we could not map (need human judgment)

Hand-written pages (home, section _index landings, merges, split, connections
matrix additions, Reference cards, stubs) are NOT touched here.
"""
import os
import re
import csv
import posixpath

import iamap
import convert as C

HERE = os.path.dirname(os.path.abspath(__file__))


def _find_root(start):
    """Walk up until we find a dir containing both docs/ and website/ (repo root)."""
    d = start
    while d != os.path.dirname(d):
        if os.path.isdir(os.path.join(d, "docs")) and os.path.isdir(os.path.join(d, "website")):
            return d
        d = os.path.dirname(d)
    raise SystemExit("could not locate repo root (needs docs/ + website/)")


ROOT = _find_root(HERE)
DOCS = os.path.join(ROOT, "docs")
CONTENT = os.path.join(ROOT, "website", "content")
OUT = HERE  # reports (link-map.csv, unmapped-links.csv) live beside the scripts

url_index = iamap.build_url_index()
unmapped = []
link_map_rows = []  # (old_src_rel, new_url)


def read(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def write(dest_rel, text):
    path = os.path.join(CONTENT, dest_rel)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)


def migrate_page(src_rel, dest_rel, title, link, weight, desc, extra_fm=None):
    text = read(os.path.join(DOCS, src_rel))
    fm, body = C.strip_frontmatter(text)
    h1_title, body = C.strip_h1(body)
    if not title:
        title = h1_title or dest_rel
    body = C.convert_body(body, src_rel, url_index, unmapped,
                          {}, iamap.ANCHOR_REDIRECTS)
    front = C.inject_frontmatter(title, link, weight, desc, extra_fm)
    write(dest_rel, front + "\n" + body.lstrip("\n"))
    link_map_rows.append((src_rel, iamap.page_url(dest_rel)))


def migrate_simple_pages():
    for src, m in iamap.PAGES.items():
        migrate_page(src, m["dest"], m["title"], m["link"], m["weight"], m["desc"])


def migrate_why():
    # Root-level leaf page: needs type=docs directly (no section cascade), and a
    # top-level navbar entry to sit beside Home in the IA.
    migrate_page(
        "why-leoflow.md", "why-leoflow.md", "Why Dexaflow", "Why Dexaflow", 5,
        "The five wounds Airflow won't heal, and how Dexaflow heals them.",
        {"type": "docs", "menu": "{ main: { weight: 5 } }"},
    )


def migrate_adrs():
    files = sorted(f for f in os.listdir(os.path.join(DOCS, "adr")) if f.endswith(".md"))
    for i, fn in enumerate(files, start=1):
        src_rel = f"adr/{fn}"
        text = read(os.path.join(DOCS, src_rel))
        fm, body = C.strip_frontmatter(text)
        h1, body = C.strip_h1(body)
        # h1 like "ADR 0001: Why Dexaflow and Not ..."; linkTitle keeps the number.
        title = h1 or fn[:-3]
        m = re.match(r"ADR\s+(\d+):\s*(.+)", title)
        if m:
            link = f"{int(m.group(1)):04d} · {m.group(2)}"
        else:
            link = title
        desc = title
        body = C.convert_body(body, src_rel, url_index, unmapped, {}, iamap.ANCHOR_REDIRECTS)
        front = C.inject_frontmatter(title, link, i * 10, desc)
        dest_rel = f"project/adrs/{fn}"
        write(dest_rel, front + "\n" + body.lstrip("\n"))
        link_map_rows.append((src_rel, iamap.page_url(dest_rel)))


def migrate_connections():
    files = sorted(
        f for f in os.listdir(os.path.join(DOCS, "connections"))
        if f.endswith(".md") and f != "index.md"
    )
    for i, fn in enumerate(files, start=1):
        src_rel = f"connections/{fn}"
        text = read(os.path.join(DOCS, src_rel))
        fm, body = C.strip_frontmatter(text)
        h1, body = C.strip_h1(body)
        title = h1 or fn[:-3]
        # linkTitle: drop the trailing " connection[s]" noise for the sidebar
        link = re.sub(r"\s+connections?$", "", title, flags=re.I).strip()
        desc = title
        body = C.convert_body(body, src_rel, url_index, unmapped, {}, iamap.ANCHOR_REDIRECTS)
        front = C.inject_frontmatter(title, link if link != title else None, i * 10, desc)
        dest_rel = f"connections/{fn}"
        write(dest_rel, front + "\n" + body.lstrip("\n"))
        link_map_rows.append((src_rel, iamap.page_url(dest_rel)))


def emit_reports():
    os.makedirs(OUT, exist_ok=True)
    with open(os.path.join(OUT, "link-map.csv"), "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["old_source_path", "new_hugo_url"])
        for old, new in sorted(link_map_rows):
            w.writerow([f"docs/{old}", new])
        # merges/splits/landings/stubs that aren't 1:1 copies
        for old, new in sorted(iamap.EXTRA_URLS.items()):
            w.writerow([f"docs/{old}", new])
    # de-dup unmapped
    seen = set()
    rows = []
    for src, target, resolved in unmapped:
        key = (src, target)
        if key in seen:
            continue
        seen.add(key)
        rows.append((src, target, resolved))
    with open(os.path.join(OUT, "unmapped-links.csv"), "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["source_page", "link_target", "resolved_path"])
        for r in sorted(rows):
            w.writerow(r)
    return len(rows)


if __name__ == "__main__":
    migrate_simple_pages()
    migrate_why()
    migrate_adrs()
    migrate_connections()
    n_unmapped = emit_reports()
    print(f"migrated PAGES:       {len(iamap.PAGES)}")
    print(f"migrated ADRs:        58")
    print(f"migrated connections: 54")
    print(f"link-map rows:        {len(link_map_rows) + len(iamap.EXTRA_URLS)}")
    print(f"unmapped links:       {n_unmapped}  (see spec/out/unmapped-links.csv)")
