import convert as C


def test_admonition_note_to_info():
    src = '!!! note "Heads up"\n    body line one\n    body line two\n\nafter\n'
    out = C.convert_admonitions(src)
    assert '{{% alert title="Heads up" color="info" %}}' in out
    assert "body line one\nbody line two" in out
    assert "{{% /alert %}}" in out
    assert "after" in out


def test_admonition_types_map():
    for typ, color in [("warning", "warning"), ("tip", "success"),
                        ("success", "success"), ("danger", "danger"), ("info", "info")]:
        out = C.convert_admonitions(f'!!! {typ}\n    x\n')
        assert f'color="{color}"' in out, (typ, out)
        # default title is capitalized type
        assert f'title="{typ.capitalize()}"' in out


def test_admonition_code_fence_body():
    src = '!!! tip "T"\n    ```yaml\n    a: 1\n    ```\n'
    out = C.convert_admonitions(src)
    assert "```yaml\na: 1\n```" in out


def test_tabs_basic():
    src = '=== "A"\n\n    line a\n\n=== "B"\n\n    line b\n'
    out = C.convert_tabs(src)
    assert "{{< tabpane text=true >}}" in out
    assert '{{% tab header="A" %}}' in out
    assert '{{% tab header="B" %}}' in out
    assert "line a" in out and "line b" in out
    assert "{{< /tabpane >}}" in out
    assert out.count("{{% /tab %}}") == 2


def test_tabs_with_code():
    src = '=== "yaml"\n\n    ```yaml\n    dag_id: x\n    ```\n'
    out = C.convert_tabs(src)
    assert "```yaml\ndag_id: x\n```" in out


def test_link_rewrite_same_dir():
    idx = {"editions.md": "/concepts/editions/"}
    un = []
    out = C.rewrite_links("[E](editions.md)", "index.md", idx, un, {}, {})
    assert out == "[E](/concepts/editions/)"
    assert un == []


def test_link_rewrite_adr_and_updir():
    idx = {}
    un = []
    out = C.rewrite_links("see [x](../adr/0035-foo.md#bar)", "connections/aws.md", idx, un, {}, {})
    assert out == "see [x](/project/adrs/0035-foo/#bar)"


def test_link_anchor_redirect():
    idx = {}
    un = []
    ar = {("concepts.md", "glossary"): "/reference/glossary/"}
    out = C.rewrite_links("[g](concepts.md#glossary)", "quickstart.md", idx, un, {}, ar)
    assert out == "[g](/reference/glossary/)"


def test_link_external_untouched():
    un = []
    s = "[gh](https://github.com/x) and [img](img.png) and ![a](b.png)"
    out = C.rewrite_links(s, "x.md", {}, un, {}, {})
    assert out == s
    assert un == []


def test_link_unmapped_recorded():
    un = []
    out = C.rewrite_links("[m](mystery.md)", "x.md", {}, un, {}, {})
    assert out == "[m](mystery.md)"
    assert un and un[0][1] == "mystery.md"


def test_convert_buttons():
    src = "[Get started](/get-started/quickstart/){ .md-button .md-button--primary } [Docs](/reference/){ .md-button }"
    out = C.convert_buttons(src)
    assert '<a class="btn btn-lg btn-primary me-3 mb-3" href="/get-started/quickstart/">Get started</a>' in out
    assert '<a class="btn btn-lg btn-secondary me-3 mb-3" href="/reference/">Docs</a>' in out


def test_buttons_run_after_link_rewrite():
    # end-to-end: md-button link with a .md target is rewritten THEN buttonized
    idx = {"quickstart.md": "/get-started/quickstart/"}
    un = []
    body = "[Get started](quickstart.md){ .md-button .md-button--primary }"
    out = C.convert_body(body, "why-leoflow.md", idx, un, {}, {})
    assert 'href="/get-started/quickstart/"' in out
    assert "md-button" not in out
    assert un == []


def test_strip_h1_with_attr():
    body = "# Dexaflow { .home-hero-title }\n\nrest\n"
    title, rest = C.strip_h1(body)
    assert title == "Dexaflow"
    assert rest.strip() == "rest"


if __name__ == "__main__":
    import sys
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    fail = 0
    for fn in fns:
        try:
            fn()
            print("ok  ", fn.__name__)
        except AssertionError as e:
            fail += 1
            print("FAIL", fn.__name__, e)
    print(f"\n{len(fns)-fail}/{len(fns)} passed")
    sys.exit(1 if fail else 0)
