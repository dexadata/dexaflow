"""
Markdown -> Hugo/Docsy transforms for the Dexaflow docs migration.

Pure text transforms, each independently testable:
  * strip_frontmatter / strip_h1  -> derive title, drop the source hero
  * inject_frontmatter            -> Hugo front-matter block
  * convert_admonitions           -> !!! type "T" (indented body) -> {{% alert %}}
  * convert_tabs                  -> === "X" (indented body) -> {{< tabpane >}}...
  * rewrite_links                 -> [x](foo.md) -> new Hugo URL (records misses)

Mermaid fences are left untouched (the baseof.html cache fix renders them).
"""
import os
import re
import posixpath

# MkDocs admonition type -> Docsy Bootstrap alert color
ADMONITION_COLOR = {
    "note": "info",
    "info": "info",
    "abstract": "info",
    "tip": "success",
    "success": "success",
    "warning": "warning",
    "caution": "warning",
    "danger": "danger",
    "error": "danger",
    "example": "info",
    "quote": "secondary",
}


def strip_frontmatter(text):
    """Return (frontmatter_dict_text_or_None, body)."""
    if text.startswith("---\n"):
        end = text.find("\n---\n", 4)
        if end != -1:
            return text[4:end], text[end + 5:]
    return None, text


def strip_h1(body):
    """Pull the leading '# Title' (optionally with {.attr}) off; return (title, rest)."""
    m = re.match(r"\s*#\s+(.+?)\s*(?:\{[^}]*\})?\s*\n", body)
    if m:
        title = m.group(1).strip()
        # drop any trailing {.attr} the regex left inside the title
        title = re.sub(r"\s*\{[^}]*\}\s*$", "", title).strip()
        return title, body[m.end():]
    return None, body


def _yaml_quote(s):
    if s is None:
        return '""'
    if re.search(r'[:#\[\]{}",&*?|<>=!%@`]', s) or s != s.strip():
        return '"' + s.replace('\\', '\\\\').replace('"', '\\"') + '"'
    return s


def inject_frontmatter(title, link, weight, desc, extra=None):
    lines = ["---"]
    lines.append(f"title: {_yaml_quote(title)}")
    if link and link != title:
        lines.append(f"linkTitle: {_yaml_quote(link)}")
    if weight is not None:
        lines.append(f"weight: {weight}")
    if desc:
        lines.append(f"description: {_yaml_quote(desc)}")
    for k, v in (extra or {}).items():
        lines.append(f"{k}: {v}")
    lines.append("---")
    return "\n".join(lines) + "\n"


# --- admonitions -------------------------------------------------------------
_ADM_RE = re.compile(r'^(?P<indent>[ \t]*)!!!\s+(?P<type>[\w-]+)(?:\s+"(?P<title>[^"]*)")?\s*$')


def _dedent_block(lines, base_indent_len):
    out = []
    for ln in lines:
        if ln.strip() == "":
            out.append("")
        else:
            out.append(ln[base_indent_len:] if len(ln) >= base_indent_len else ln.lstrip())
    return out


def convert_admonitions(text):
    lines = text.split("\n")
    out = []
    i = 0
    n = len(lines)
    while i < n:
        m = _ADM_RE.match(lines[i])
        if not m:
            out.append(lines[i])
            i += 1
            continue
        indent = m.group("indent")
        atype = m.group("type").lower()
        color = ADMONITION_COLOR.get(atype, "info")
        title = m.group("title")
        if title is None:
            title = atype.capitalize()
        # gather the indented body
        i += 1
        body = []
        base = None
        while i < n:
            ln = lines[i]
            if ln.strip() == "":
                body.append("")
                i += 1
                continue
            # body must be more-indented than the marker
            stripped = ln[len(indent):] if ln.startswith(indent) else ln
            if ln.startswith(indent) and stripped[:1] in (" ", "\t"):
                body.append(ln)
                i += 1
            else:
                break
        # strip trailing blank lines in body
        while body and body[-1] == "":
            body.pop()
        # dedent by (marker indent + 4)
        base_len = len(indent) + 4
        body = _dedent_block(body, base_len)
        out.append(f'{indent}{{{{% alert title="{title}" color="{color}" %}}}}')
        out.extend(body)
        out.append(f"{indent}{{{{% /alert %}}}}")
        out.append("")
    return "\n".join(out)


# --- tabs --------------------------------------------------------------------
_TAB_RE = re.compile(r'^(?P<indent>[ \t]*)===\s+"(?P<header>[^"]*)"\s*$')


def convert_tabs(text):
    lines = text.split("\n")
    out = []
    i = 0
    n = len(lines)
    while i < n:
        m = _TAB_RE.match(lines[i])
        if not m:
            out.append(lines[i])
            i += 1
            continue
        group_indent = m.group("indent")
        # collect a run of consecutive tabs at the same indent
        tabs = []
        while i < n:
            mm = _TAB_RE.match(lines[i])
            if not mm or mm.group("indent") != group_indent:
                break
            header = mm.group("header")
            i += 1
            body = []
            while i < n:
                ln = lines[i]
                if _TAB_RE.match(ln) and _TAB_RE.match(ln).group("indent") == group_indent:
                    break
                if ln.strip() == "":
                    body.append("")
                    i += 1
                    continue
                # body of a tab is indented (>= group_indent + 4)
                if ln.startswith(group_indent + "    ") or ln.startswith(group_indent + "\t"):
                    body.append(ln)
                    i += 1
                else:
                    # de-indented, non-blank line: tabs run ends
                    break
            while body and body[-1] == "":
                body.pop()
            body = _dedent_block(body, len(group_indent) + 4)
            tabs.append((header, body))
        # emit
        out.append(f"{group_indent}{{{{< tabpane text=true >}}}}")
        for header, body in tabs:
            out.append(f'{group_indent}{{{{% tab header="{header}" %}}}}')
            out.extend(group_indent + b if b else "" for b in body)
            out.append(f"{group_indent}{{{{% /tab %}}}}")
        out.append(f"{group_indent}{{{{< /tabpane >}}}}")
        out.append("")
    return "\n".join(out)


# --- links -------------------------------------------------------------------
_LINK_RE = re.compile(r'(?<!\!)\[([^\]]*)\]\(([^)]+)\)')


def rewrite_links(text, src_rel, url_index, unmapped, adr_urls, anchor_redirects):
    """
    Rewrite inline [text](target) where target points at a .md/.html doc page.
    src_rel: source path relative to docs/ (posix).
    Records (src_rel, target) in `unmapped` for anything doc-like we can't map.
    """
    src_dir = posixpath.dirname(src_rel)

    def repl(m):
        label, target = m.group(1), m.group(2)
        # leave external, anchor-only, image, mailto, and non-doc links alone
        if target.startswith(("http://", "https://", "mailto:", "#", "/", "www.")):
            return m.group(0)
        # split off anchor / title
        anchor = ""
        if "#" in target:
            path, anchor = target.split("#", 1)
            anchor = "#" + anchor
        else:
            path = target
        # ignore pure query/anchor
        if path == "":
            return m.group(0)
        # only rewrite doc pages
        if not (path.endswith(".md") or path.endswith(".html")):
            return m.group(0)
        # resolve relative to source dir
        resolved = posixpath.normpath(posixpath.join(src_dir, path)) if src_dir else posixpath.normpath(path)
        # anchor redirect (concepts.md#glossary -> reference/glossary)
        if anchor:
            key = (resolved, anchor[1:])
            if key in anchor_redirects:
                return f"[{label}]({anchor_redirects[key]})"
        # adr pages
        new = None
        if resolved.startswith("adr/") and resolved.endswith(".md"):
            slug = posixpath.basename(resolved)[:-3]
            new = f"/project/adrs/{slug}/"
        elif resolved in url_index:
            new = url_index[resolved]
        elif resolved.startswith("connections/") and resolved.endswith(".md"):
            slug = posixpath.basename(resolved)[:-3]
            new = f"/connections/{slug}/"
        elif resolved.startswith("go/") and resolved.endswith(".md"):
            new = "/reference/go/" + resolved[len("go/"):-3] + "/"
        elif resolved.startswith("cli/") and resolved.endswith(".md"):
            new = "/reference/cli/" + resolved[len("cli/"):-3] + "/"
        if new is None:
            unmapped.append((src_rel, target, resolved))
            return m.group(0)
        return f"[{label}]({new}{anchor})"

    return _LINK_RE.sub(repl, text)


# --- images ------------------------------------------------------------------
_IMG_RE = re.compile(r'(!\[[^\]]*\]\()([^)\s]+)((?:\s+"[^"]*")?)\)(\{[^}]*\})?')


def rewrite_images(text, src_rel):
    """Make image paths site-absolute (/assets/..., /screenshots/...) and strip
    MkDocs attr-list suffixes ({ .class }) that Goldmark would render literally."""
    src_dir = posixpath.dirname(src_rel)

    def repl(m):
        pre, target, title = m.group(1), m.group(2), m.group(3)
        if target.startswith(("http://", "https://", "data:", "/")):
            return f"{pre}{target}{title})"
        resolved = posixpath.normpath(posixpath.join(src_dir, target)) if src_dir else posixpath.normpath(target)
        return f"{pre}/{resolved}{title})"

    return _IMG_RE.sub(repl, text)


# --- MkDocs button attr-lists -> Bootstrap/Docsy buttons ---------------------
_BTN_RE = re.compile(r'\[([^\]]+)\]\(([^)]+)\)\{([^}]*\.md-button[^}]*)\}')


def convert_buttons(text):
    """`[label](url){ .md-button .md-button--primary }` -> a styled <a> button.
    Runs AFTER rewrite_links, so `url` is already the final Hugo path; canonifyURLs
    then prefixes the raw <a href> under the baseURL like every other link."""
    def repl(m):
        label, url, attrs = m.group(1), m.group(2), m.group(3)
        primary = "md-button--primary" in attrs
        cls = "btn btn-lg " + ("btn-primary" if primary else "btn-secondary") + " me-3 mb-3"
        return f'<a class="{cls}" href="{url}">{label}</a>'
    return _BTN_RE.sub(repl, text)


def convert_body(body, src_rel, url_index, unmapped, adr_urls, anchor_redirects):
    body = convert_admonitions(body)
    body = convert_tabs(body)
    body = rewrite_images(body, src_rel)
    body = rewrite_links(body, src_rel, url_index, unmapped, adr_urls, anchor_redirects)
    body = convert_buttons(body)
    return body
