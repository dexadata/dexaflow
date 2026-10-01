"""Scan built public/ for broken INTERNAL links (Phase F4 QA, not committed-critical).
Resolves every site-internal href/src (absolute under BASE or root-relative)
to a file on disk under public/."""
import os, re, sys, html

PUB = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "public")
PUB = os.path.abspath(PUB)
BASE = "https://dexaflow.dexadata.ai/"
ATTR = re.compile(r'(?:href|src)=("([^"]*)"|\'([^\']*)\'|([^ >]+))', re.I)

def to_path(u):
    u = html.unescape(u)
    if u.startswith(BASE):
        u = "/" + u[len(BASE):]
    if not u.startswith("/") or u.startswith("//"):
        return None            # external / relative / anchor / mailto
    u = u[1:]
    u = u.split("#")[0].split("?")[0]
    return u

def resolves(rel):
    if rel == "" or rel.endswith("/"):
        return os.path.isfile(os.path.join(PUB, rel, "index.html"))
    p = os.path.join(PUB, rel)
    if os.path.isfile(p):
        return True
    if os.path.isfile(p + ".html"):
        return True
    return os.path.isfile(os.path.join(p, "index.html"))

broken = {}
for root, _, files in os.walk(PUB):
    for f in files:
        if not f.endswith(".html"):
            continue
        fp = os.path.join(root, f)
        with open(fp, encoding="utf-8", errors="ignore") as fh:
            doc = fh.read()
        for m in ATTR.finditer(doc):
            raw = m.group(2) or m.group(3) or m.group(4) or ""
            rel = to_path(raw)
            if rel is None:
                continue
            if not resolves(rel):
                broken.setdefault(os.path.relpath(fp, PUB), set()).add(raw)

total = sum(len(v) for v in broken.values())
print("broken internal links: %d (across %d pages)" % (total, len(broken)))
for page, links in sorted(broken.items())[:40]:
    for l in sorted(links):
        print("  %s -> %s" % (page, l))
sys.exit(1 if total else 0)
