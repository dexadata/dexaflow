package ui

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func fixture() *Server {
	return NewFromFS(fstest.MapFS{
		"index.html":           {Data: []byte(`<title>Airflow</title><base href="{{ backend_server_base_url }}" /><div id="root"></div>`)},
		"assets/app-abc123.js": {Data: []byte("console.log('hi')")},
		"sql_bg-xyz.wasm":      {Data: []byte("\x00asm")},
		"VERSION":              {Data: []byte("3.2.1\n")},
	}, "3.2.1")
}

func TestIndexTemplatesBaseHref(t *testing.T) {
	rec := httptest.NewRecorder()
	fixture().Index(rec, "/")
	body := rec.Body.String()
	if strings.Contains(body, baseHrefPlaceholder) {
		t.Errorf("base href placeholder not substituted: %q", body)
	}
	if !strings.Contains(body, `<base href="/" />`) {
		t.Errorf("expected templated base href, got %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("index cache-control = %q, want no-cache", cc)
	}
}

func TestIndexDefaultsEmptyBasePathToRoot(t *testing.T) {
	rec := httptest.NewRecorder()
	fixture().Index(rec, "")
	if !strings.Contains(rec.Body.String(), `<base href="/" />`) {
		t.Errorf("empty base path should default to /, got %q", rec.Body.String())
	}
}

// TestIndexRewritesTitleToInstanceName covers #D15: the embedded SPA's
// <title>Airflow</title> is rewritten to the configured instance name so the
// browser tab brands as Leoflow, not as Airflow. Empty instance name falls
// back to "Leoflow" (matching the default Airflow instance_name behavior).
func TestIndexRewritesTitleToInstanceName(t *testing.T) {
	cases := []struct {
		name     string
		instance string
		wantTag  string
	}{
		{"default falls back to Leoflow", "", "<title>Leoflow</title>"},
		{"custom Lite name", "Leoflow Lite", "<title>Leoflow Lite</title>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture()
			s.SetInstanceName(tc.instance)
			rec := httptest.NewRecorder()
			s.Index(rec, "/")
			body := rec.Body.String()
			if strings.Contains(body, "<title>Airflow</title>") {
				t.Errorf("stale <title>Airflow</title> not rewritten, body: %q", body)
			}
			if !strings.Contains(body, tc.wantTag) {
				t.Errorf("expected %q in body, got %q", tc.wantTag, body)
			}
		})
	}
}

// TestIndexInjectsClipboardFallback covers #242: the Airflow SPA's copy
// buttons (logs, run IDs, etc.) call navigator.clipboard.writeText, which
// throws on plain http:// LAN origins because the Clipboard API requires a
// secure context. The Leoflow shell injects a tiny polyfill so the copy
// button still works when users access Lite over `http://<host-lan-ip>:8080`.
// The polyfill is a no-op when the native API is available, so it is always
// injected.
func TestIndexInjectsClipboardFallback(t *testing.T) {
	rec := httptest.NewRecorder()
	fixture().Index(rec, "/")
	body := rec.Body.String()
	if !strings.Contains(body, "navigator.clipboard") {
		t.Errorf("polyfill missing — body should reference navigator.clipboard; got %q", body)
	}
	if !strings.Contains(body, "execCommand('copy')") {
		t.Errorf("polyfill missing — body should fall back to document.execCommand('copy'); got %q", body)
	}
}

func TestStaticServesHashedAssetImmutable(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/assets/app-abc123.js", http.NoBody)
	fixture().StaticHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("static asset = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("hashed asset cache-control = %q, want immutable", cc)
	}
}

func TestStaticServesWasmWithCorrectMIME(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/sql_bg-xyz.wasm", http.NoBody)
	fixture().StaticHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("wasm = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/wasm") {
		t.Errorf("wasm content-type = %q, want application/wasm", ct)
	}
}

func TestStaticGzipsCompressibleAssetWhenAccepted(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/assets/app-abc123.js", http.NoBody)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	fixture().StaticHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("gzip asset = %d", rec.Code)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", enc)
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Errorf("missing Vary: Accept-Encoding")
	}
	gz, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "console.log('hi')" {
		t.Errorf("decompressed body = %q", body)
	}
}

func TestStaticNoGzipWhenNotAccepted(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/assets/app-abc123.js", http.NoBody)
	fixture().StaticHandler().ServeHTTP(rec, req) // no Accept-Encoding
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want none", enc)
	}
	if rec.Body.String() != "console.log('hi')" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestStaticMissingFileIs404(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/assets/nope.js", http.NoBody)
	fixture().StaticHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing static file = %d, want 404 (no SPA fallback under /static)", rec.Code)
	}
}

func TestEmbeddedBundleHasIndex(t *testing.T) {
	// The committed placeholder (or fetched bundle) must always carry index.html
	// so the binary builds and serves a shell.
	rec := httptest.NewRecorder()
	New().Index(rec, "/")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "root") {
		t.Errorf("embedded index = %d, body=%q", rec.Code, rec.Body.String())
	}
}

func TestIndexInjectsDevBannerOnlyInDevMode(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte(`<body><div id="root"></div></body>`)}}

	dev := NewFromFS(fsys, "v")
	dev.SetLiteBanner(true)
	rec := httptest.NewRecorder()
	dev.Index(rec, "/")
	body := rec.Body.String()
	if !strings.Contains(body, "leoflow-lite-banner") || !strings.Contains(body, ">LITE<") {
		t.Errorf("lite mode must inject the LITE overlay, got:\n%s", body)
	}
	// The overlay goes before </body> so it renders on top of the SPA.
	if strings.Index(body, "leoflow-lite-banner") > strings.Index(body, "</body>") {
		t.Error("LITE overlay should be injected before </body>")
	}

	off := NewFromFS(fsys, "v") // dev banner not set → demo/prod
	rec2 := httptest.NewRecorder()
	off.Index(rec2, "/")
	if strings.Contains(rec2.Body.String(), "leoflow-lite-banner") {
		t.Error("non-lite mode must NOT inject the LITE overlay")
	}
}

func TestIndexInjectsProBannerOnlyInProMode(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte(`<body><div id="root"></div></body>`)}}

	pro := NewFromFS(fsys, "v")
	pro.SetProBanner(true)
	rec := httptest.NewRecorder()
	pro.Index(rec, "/")
	body := rec.Body.String()
	if !strings.Contains(body, "leoflow-pro-banner") || !strings.Contains(body, ">PRO<") {
		t.Errorf("pro mode must inject the PRO overlay, got:\n%s", body)
	}
	if strings.Index(body, "leoflow-pro-banner") > strings.Index(body, "</body>") {
		t.Error("PRO overlay should be injected before </body>")
	}

	off := NewFromFS(fsys, "v") // pro banner not set → demo/lite
	rec2 := httptest.NewRecorder()
	off.Index(rec2, "/")
	if strings.Contains(rec2.Body.String(), "leoflow-pro-banner") {
		t.Error("non-pro mode must NOT inject the PRO overlay")
	}

	// Defensive: the two badges are mutually exclusive; the server should never
	// flip both on at the same time, but if it did, neither should silently win.
	both := NewFromFS(fsys, "v")
	both.SetLiteBanner(true)
	both.SetProBanner(true)
	rec3 := httptest.NewRecorder()
	both.Index(rec3, "/")
	if !strings.Contains(rec3.Body.String(), ">LITE<") || !strings.Contains(rec3.Body.String(), ">PRO<") {
		t.Error("both banners enabled: both pills should be in the body (server is the source of truth)")
	}
}

func TestIndexInjectsEditorButtonOnlyWhenEnabled(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte(`<body><div id="root"></div></body>`)}}

	on := NewFromFS(fsys, "v")
	on.SetEditorButton(true)
	rec := httptest.NewRecorder()
	on.Index(rec, "/")
	body := rec.Body.String()
	if !strings.Contains(body, "leoflow-ide-button") || !strings.Contains(body, `href="/ide"`) {
		t.Errorf("editor enabled must inject an IDE button linking to /ide, got:\n%s", body)
	}
	if !strings.Contains(body, `target="_blank"`) {
		t.Error("the IDE button should open in a new tab")
	}
	if strings.Index(body, "leoflow-ide-button") > strings.Index(body, "</body>") {
		t.Error("IDE button should be injected before </body>")
	}
	// Regression guard (#88): the button must carry a visible inline SVG icon and
	// the IDE label. A bare unicode glyph rendered faintly/not at all on some
	// platforms, so if the icon is ever dropped or reverted to a glyph, fail.
	if !strings.Contains(body, "<svg") || !strings.Contains(body, "polyline") {
		t.Errorf("the IDE button must include its inline SVG icon (not a unicode glyph), got:\n%s", body)
	}
	if !strings.Contains(body, ">IDE<") {
		t.Error("the IDE button must show the 'IDE' label")
	}

	off := NewFromFS(fsys, "v")
	rec2 := httptest.NewRecorder()
	off.Index(rec2, "/")
	if strings.Contains(rec2.Body.String(), "leoflow-ide-button") {
		t.Error("editor disabled must NOT inject the IDE button")
	}
}

// TestIndexInjectsHomeLinkOnlyWhenSet covers #1290: an operator who serves
// Leoflow inside a larger platform can give users a persistent way back. The
// link opens in the same tab (it is the way back, not a side trip) and is off
// unless configured.
func TestIndexInjectsHomeLinkOnlyWhenSet(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte(`<body><div id="root"></div></body>`)}}

	on := NewFromFS(fsys, "v")
	on.SetHomeLink("Back to portal", "https://portal.example.com/team")
	rec := httptest.NewRecorder()
	on.Index(rec, "/")
	body := rec.Body.String()
	if !strings.Contains(body, `id="leoflow-home-link"`) || !strings.Contains(body, `href="https://portal.example.com/team"`) {
		t.Fatalf("home link set: want an anchor to the configured URL, got:\n%s", body)
	}
	if !strings.Contains(body, ">Back to portal<") {
		t.Error("home link must show the configured label")
	}
	if strings.Contains(body, `target="_blank"`) {
		t.Error("home link must open in the same tab")
	}
	if strings.Index(body, "leoflow-home-link") > strings.Index(body, "</body>") {
		t.Error("home link should be injected before </body>")
	}

	off := NewFromFS(fsys, "v")
	rec2 := httptest.NewRecorder()
	off.Index(rec2, "/")
	if strings.Contains(rec2.Body.String(), "leoflow-home-link") {
		t.Error("no home link configured must inject nothing")
	}
}

// TestIndexHomeLinkEscapesItsValues locks that config values reach the page as
// text, never as markup: a label or URL carrying quotes or tags must not break
// out of the attribute or the element.
func TestIndexHomeLinkEscapesItsValues(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte(`<body></body>`)}}
	s := NewFromFS(fsys, "v")
	s.SetHomeLink(`<script>alert(1)</script>`, `https://portal.example.com/?a="><script>x</script>`)
	rec := httptest.NewRecorder()

	s.Index(rec, "/")

	body := rec.Body.String()
	if strings.Contains(body, "<script>alert(1)") || strings.Contains(body, `"><script>x`) {
		t.Errorf("home link values were not escaped:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("label should be HTML-escaped text, got:\n%s", body)
	}
}

// TestIndexBrandsFaviconAndStylesheets covers #1289's shell half: the
// favicon link points at the configured URL and each extra stylesheet (web
// fonts, overrides) loads in <head>, escaped. Unset leaves the shell as is.
func TestIndexBrandsFaviconAndStylesheets(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte(
		`<head><link rel="icon" type="image/png" href="./static/pin_32.png" /></head><body></body>`)}}

	s := NewFromFS(fsys, "v")
	s.SetFavicon("https://cdn.example.com/f.png")
	s.SetStylesheets([]string{"https://fonts.example.com/a.css", `https://x.example/b.css?"><script>`})
	rec := httptest.NewRecorder()
	s.Index(rec, "/")
	body := rec.Body.String()
	if !strings.Contains(body, `href="https://cdn.example.com/f.png"`) || strings.Contains(body, "pin_32.png") {
		t.Errorf("favicon not rebranded:\n%s", body)
	}
	if !strings.Contains(body, `<link rel="stylesheet" href="https://fonts.example.com/a.css">`) {
		t.Errorf("stylesheet link missing:\n%s", body)
	}
	if strings.Contains(body, `"><script>`) {
		t.Errorf("stylesheet URL not escaped:\n%s", body)
	}
	if strings.Index(body, "a.css") > strings.Index(body, "</head>") {
		t.Error("stylesheets must load in <head>")
	}

	plain := NewFromFS(fsys, "v")
	rec2 := httptest.NewRecorder()
	plain.Index(rec2, "/")
	if !strings.Contains(rec2.Body.String(), "pin_32.png") || strings.Contains(rec2.Body.String(), `rel="stylesheet"`) {
		t.Error("no branding configured must leave the favicon and add no stylesheet")
	}
}

// TestEmbeddedBundleFaviconIsRebranded guards the anchor the favicon rewrite
// depends on: a bundle upgrade that changes the favicon tag must fail here, not
// silently keep the stock icon in production.
func TestEmbeddedBundleFaviconIsRebranded(t *testing.T) {
	s := New()
	s.SetFavicon("/brand/favicon.svg")
	rec := httptest.NewRecorder()

	s.Index(rec, "/")

	body := rec.Body.String()
	if !strings.Contains(body, `<link rel="icon" href="/brand/favicon.svg" />`) || strings.Contains(body, "pin_32.png") {
		t.Errorf("embedded bundle favicon was not rewritten:\n%s", body)
	}
}
