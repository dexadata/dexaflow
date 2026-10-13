#!/usr/bin/env python3
"""Render a Hugo config OVERLAY for one leg of the multi-version Pages build
(issue #814).

Why an overlay instead of editing hugo.toml in place: the four legs (latest
GA / dev / each archived tag) are built from FOUR DIFFERENT git refs, and an
archived tag's own website/hugo.toml predates this versioning scheme (it may
have only the old cosmetic dev/latest pair, or none at all). We never rely on
the checked-out ref's hugo.toml to already know about the other legs. Instead
this script reads website/scripts/ci/versions.json from THIS ref (the ref
that triggered the workflow, i.e. always the current tooling) and renders a
second TOML file. The build then runs:

    hugo --config hugo.toml,<this-overlay> --baseURL <leg-url> ...

Hugo merges multiple --config files left-to-right with the LAST file winning
per key (arrays are replaced wholesale, not appended) — verified locally
against hugo v0.165.0. So the overlay fully determines baseURL, the active
version label, the archived-version banner, and the complete version
dropdown for every leg, regardless of what that leg's own hugo.toml contains.

The "dev" leg's ref in versions.json is the empty string (meaning "whatever
ref triggered this workflow run"); that resolution happens in the workflow's
`plan` job (via jq, against github.sha), not here — this script only cares
about labels/subpaths/archived flags, which are static.

The same manifest also has to reach website/hugo.toml, whose
[[params.versions]] block is the fallback a plain `hugo` build uses. Nothing
kept the two in step: scripts/cut-release.sh repointed versions.json on every
GA and left hugo.toml alone, so check-docs-version-menu.sh failed the docs
promotion PR of every release, and while that PR sat unmerged the published
release had its documentation root still serving the previous GA (#1242).

--sync-hugo-config rewrites that block from the manifest, reusing the same
build_url and the same entry order as the overlay above, so the fallback and
the published site cannot disagree by construction.

Usage:
    render-version-config.py --versions-file website/scripts/ci/versions.json \\
        --leg-id v0.4.0 --out overlay.toml
    render-version-config.py --versions-file website/scripts/ci/versions.json \\
        --sync-hugo-config website/hugo.toml
    render-version-config.py --self-test
"""
import re
import argparse
import json
import sys


def build_url(root_url: str, subpath: str) -> str:
    root = root_url.rstrip("/") + "/"
    if not subpath:
        return root
    return root + subpath.strip("/") + "/"


def toml_string(value: str) -> str:
    # Values here are URLs / version labels — no embedded quotes/newlines expected,
    # but escape defensively rather than assume.
    escaped = value.replace("\\", "\\\\").replace('"', '\\"')
    return f'"{escaped}"'


def render_versions_block(root_url: str, entries: list) -> str:
    """The [[params.versions]] array, as hugo.toml spells it."""
    lines = []
    for e in entries:
        lines.append("  [[params.versions]]")
        lines.append(f"    version = {toml_string(e['label'])}")
        lines.append(f"    url = {toml_string(build_url(root_url, e['subpath']))}")
    return "\n".join(lines) + "\n"


# One [[params.versions]] entry as hugo.toml writes it. Anchored per line so a
# comment mentioning the array, or any other params block, is never eaten.
_ENTRY_RE = re.compile(
    r"^  \[\[params\.versions\]\]\n    version = .*\n    url = .*\n", re.M
)


def sync_hugo_config(path: str, root_url: str, entries: list) -> int:
    """Replace the [[params.versions]] run in `path` with the manifest's."""
    with open(path, encoding="utf-8") as f:
        text = f.read()
    found = list(_ENTRY_RE.finditer(text))
    if not found:
        # Never silently append: a file whose block moved or was renamed is a
        # file this script no longer understands, and writing anyway would put
        # the menu somewhere Hugo does not read.
        print(f"error: no [[params.versions]] block found in {path}", file=sys.stderr)
        return 1
    start, end = found[0].start(), found[-1].end()
    new = text[:start] + render_versions_block(root_url, entries) + text[end:]
    if new == text:
        print(f"render-version-config: {path} already matches the manifest")
        return 0
    with open(path, "w", encoding="utf-8") as f:
        f.write(new)
    print(f"render-version-config: synced {len(entries)} version(s) into {path}")
    return 0


def latest_release(entries: list) -> str:
    """The newest GA without its leading v: the `latest` leg's ref."""
    ref = next((e.get("ref", "") for e in entries if e.get("id") == "latest"), "")
    return ref[1:] if ref.startswith("v") else ref


def render_overlay(root_url: str, entries: list, leg_id: str) -> str:
    """The Hugo config overlay for one leg. Raises KeyError for an unknown leg."""
    this = next((e for e in entries if e["id"] == leg_id), None)
    if this is None:
        raise KeyError(leg_id)
    lines = [
        f"baseURL = {toml_string(build_url(root_url, this['subpath']))}",
        "[params]",
        f"  version = {toml_string(this['label'])}",
        '  version_menu = "Releases"',
        f"  archived_version = {'true' if this['archived'] else 'false'}",
        f"  url_latest_version = {toml_string(root_url)}",
        f"  latest_release = {toml_string(latest_release(entries))}",
    ]
    for e in entries:
        lines.append("  [[params.versions]]")
        lines.append(f"    version = {toml_string(e['label'])}")
        lines.append(f"    url = {toml_string(build_url(root_url, e['subpath']))}")
    return "\n".join(lines) + "\n"


def self_test() -> int:
    import tempfile, os
    fails = []

    def eq(got, want, name):
        if got == want:
            print(f"  ok   {name}")
        else:
            print(f"  FAIL {name}\n    got:  {got!r}\n    want: {want!r}")
            fails.append(name)

    entries = [
        {"id": "latest", "ref": "v9.9.9", "subpath": "", "label": "v9.9.9 (latest)", "archived": False},
        {"id": "v9.9.8", "ref": "v9.9.8", "subpath": "v9.9.8", "label": "v9.9.8", "archived": True},
    ]
    root = "https://example.invalid/leoflow/"

    # Every leg learns the newest GA, whatever ref it is built from: the
    # {{< latest-version >}} shortcode prints it (#1555). The latest leg is
    # built from the GA's own tag, whose versions.json still names the GA
    # before it, so the value has to come from this overlay.
    eq(latest_release(entries), "9.9.9", "the newest GA is the latest leg's ref, without the v")
    eq(latest_release([{"id": "dev", "ref": ""}]), "", "no latest leg gives no value")
    for leg in ("latest", "v9.9.8"):
        overlay = render_overlay(root, entries, leg)
        eq('  latest_release = "9.9.9"' in overlay.splitlines(), True, f"the {leg} overlay carries latest_release")

    eq(build_url(root, ""), "https://example.invalid/leoflow/", "root leg keeps the root url")
    eq(build_url(root, "v9.9.8"), "https://example.invalid/leoflow/v9.9.8/", "an archived leg gets its subpath")

    with tempfile.TemporaryDirectory() as d:
        p = os.path.join(d, "hugo.toml")
        # Surrounding content must survive: the rewrite is a block replacement,
        # not a file rewrite.
        with open(p, "w", encoding="utf-8") as f:
            f.write(
                "[params]\n  before = true\n"
                '  [[params.versions]]\n    version = "OLD (latest)"\n    url = "https://old/"\n'
                '  [[params.versions]]\n    version = "OLDER"\n    url = "https://older/"\n'
                "  after = true\n"
            )
        eq(sync_hugo_config(p, root, entries), 0, "sync returns 0 on a well formed file")
        out = open(p, encoding="utf-8").read()
        eq(out.count("[[params.versions]]"), 2, "one entry per manifest version")
        eq("OLD (latest)" in out, False, "the stale entries are gone")
        eq('version = "v9.9.9 (latest)"' in out, True, "the new latest is written")
        eq("before = true" in out and "after = true" in out, True, "content around the block survives")
        # Idempotent: the cut runs on every release and must not churn the file.
        eq(sync_hugo_config(p, root, entries), 0, "a second sync is a no-op")
        eq(open(p, encoding="utf-8").read(), out, "and leaves the file byte for byte")

        # A file with no block is an error, never a silent append.
        q = os.path.join(d, "empty.toml")
        open(q, "w", encoding="utf-8").write("[params]\n  nothing = true\n")
        eq(sync_hugo_config(q, root, entries), 1, "a file with no block fails instead of appending")

    print("self-test: PASS" if not fails else "self-test: FAIL")
    return 0 if not fails else 1


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--versions-file")
    ap.add_argument("--leg-id", help="id of the leg being built (matches versions.json)")
    ap.add_argument("--out", help="path to write the overlay TOML")
    ap.add_argument("--sync-hugo-config", help="rewrite the [[params.versions]] block of this hugo.toml from the manifest")
    ap.add_argument("--self-test", action="store_true")
    args = ap.parse_args()

    if args.self_test:
        return self_test()
    if not args.versions_file:
        ap.error("--versions-file is required")

    with open(args.versions_file, encoding="utf-8") as f:
        manifest = json.load(f)

    root_url = manifest["root_url"]
    entries = manifest["versions"]

    if args.sync_hugo_config:
        return sync_hugo_config(args.sync_hugo_config, root_url, entries)

    if not args.leg_id or not args.out:
        ap.error("--leg-id and --out are required unless --sync-hugo-config is given")

    try:
        overlay = render_overlay(root_url, entries, args.leg_id)
    except KeyError:
        print(f"error: leg id {args.leg_id!r} not found in {args.versions_file}", file=sys.stderr)
        return 1
    leg_url = build_url(root_url, next(e for e in entries if e["id"] == args.leg_id)["subpath"])

    with open(args.out, "w", encoding="utf-8") as f:
        f.write(overlay)

    print(f"render-version-config: wrote {args.out} (leg={args.leg_id}, baseURL={leg_url})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
