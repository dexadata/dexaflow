package ui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// baseHrefPlaceholder is the Jinja token Airflow leaves in index.html for the
// server to fill with the deployment base path. Dexaflow substitutes it at
// request time, mirroring Airflow's TemplateResponse.
const baseHrefPlaceholder = "{{ backend_server_base_url }}"

// clipboardFallbackHTML defines navigator.clipboard.writeText when the native
// Clipboard API is unavailable. The API is gated behind a secure context, so
// users hitting Lite over a plain http://<lan-ip>:8080 origin (a real Lima /
// dogfood scenario) see the SPA's copy buttons silently fail (#242). The
// fallback uses the legacy document.execCommand('copy') + offscreen textarea
// trick. On https / localhost the native API is present and the polyfill is
// a no-op, so it is always injected.
const clipboardFallbackHTML = `<script>` +
	`(function(){` +
	`if(window.isSecureContext&&navigator.clipboard&&navigator.clipboard.writeText)return;` +
	`var w=function(t){return new Promise(function(r,e){` +
	`var ta=document.createElement('textarea');ta.value=t;` +
	`ta.setAttribute('readonly','');ta.style.position='fixed';ta.style.top='-1000px';` +
	`document.body.appendChild(ta);ta.select();` +
	`try{document.execCommand('copy');r()}catch(x){e(x)}` +
	`finally{document.body.removeChild(ta)}` +
	`})};` +
	`if(!navigator.clipboard)navigator.clipboard={writeText:w};` +
	`else if(!navigator.clipboard.writeText)navigator.clipboard.writeText=w;` +
	`})();` +
	`</script>`

// liteBannerHTML is a discreet, neutral-gray "LITE" pill fixed at top-center,
// injected into the served shell in the Lite edition so the local environment is
// never mistaken for production. The slate gray (with white text) reads well on
// both the light and dark UI themes; pointer-events:none keeps it click-through.
const liteBannerHTML = `<div id="leoflow-lite-banner">LITE</div>` +
	`<style>#leoflow-lite-banner{position:fixed;top:0;left:50%;transform:translateX(-50%);` +
	`z-index:2147483647;background:rgba(100,116,139,.92);color:#fff;` +
	`font:600 11px/1.7 system-ui,-apple-system,sans-serif;padding:1px 16px;` +
	`border-radius:0 0 6px 6px;letter-spacing:3px;pointer-events:none}</style>`

// proBannerHTML is the gold counterpart to liteBannerHTML, injected when the
// running edition is "pro". Same shape and placement as the Lite pill so
// operators get a consistent visual anchor across editions; only the color
// shifts (gold #FFD700 with dark text), matching the gold edition badge in
// docs/editions.md and the README's edition shield. pointer-events:none keeps
// it click-through.
const proBannerHTML = `<div id="leoflow-pro-banner">PRO</div>` +
	`<style>#leoflow-pro-banner{position:fixed;top:0;left:50%;transform:translateX(-50%);` +
	`z-index:2147483647;background:#FFD700;color:#1a1a1a;` +
	`font:600 11px/1.7 system-ui,-apple-system,sans-serif;padding:1px 16px;` +
	`border-radius:0 0 6px 6px;letter-spacing:3px;pointer-events:none}</style>`

// ideButtonHTML is a discreet floating "IDE" button (bottom-right) injected into
// the served shell when the Lite web editor is enabled (ADR 0025). It opens the
// editor at /ide in a new tab, so the SPA is untouched.
//
// It matches the UI's native button identity (sampled from the app's active nav
// button): the same accent color, Inter font, weight, and radius — so it reads
// as part of the app rather than a foreign control, and looks right on both the
// light and dark themes (a solid accent fill with white text works on either).
// The icon is an inline "code" (< >) SVG drawn with currentColor, so it renders
// crisply at any size regardless of the system font (a Unicode glyph rendered
// faintly or not at all on some platforms). The accent has a hex fallback before
// the oklch the app uses, for browsers without oklch support.
const ideButtonHTML = `<a id="leoflow-ide-button" href="/ide" target="_blank" rel="noopener" title="Open the Dexaflow editor">` +
	`<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" ` +
	`stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">` +
	`<polyline points="8 7 3 12 8 17"></polyline><polyline points="16 7 21 12 16 17"></polyline></svg>` +
	`<span>IDE</span></a>` +
	`<style>#leoflow-ide-button{position:fixed;right:16px;bottom:16px;z-index:2147483647;` +
	`display:inline-flex;align-items:center;gap:7px;` +
	`background:#3f5c91;background:oklch(0.469 0.084 257.657);color:#fff;text-decoration:none;` +
	`font:500 14px/1 Inter,-apple-system,system-ui,"Segoe UI",Helvetica,Arial,sans-serif;` +
	`padding:9px 15px;border-radius:8px;box-shadow:0 2px 8px rgba(0,0,0,.25)}` +
	`#leoflow-ide-button:hover{background:#35507f;background:oklch(0.42 0.084 257.657)}` +
	`#leoflow-ide-button svg{display:block}</style>`

// homeLinkStyle styles the operator home link (#1290): a small floating pill at
// the bottom-left, just right of the SPA's 64px navigation column, mirroring
// the IDE button at the bottom-right. The top-right looked free but holds the
// DAG page's Trigger button. It borrows the UI's native typography; the fill
// inverts with the SPA's dark mode (the "dark" class Chakra sets on <html>) so
// the pill keeps its contrast on either theme.
const homeLinkStyle = `<style>#leoflow-home-link{position:fixed;left:76px;bottom:16px;z-index:2147483646;` +
	`display:inline-flex;align-items:center;gap:6px;max-width:240px;` +
	`font:500 13px/1 Inter,-apple-system,system-ui,"Segoe UI",Helvetica,Arial,sans-serif;` +
	`color:#fff;background:rgba(15,23,42,.82);text-decoration:none;` +
	`padding:7px 12px;border-radius:999px;box-shadow:0 2px 8px rgba(0,0,0,.2)}` +
	`#leoflow-home-link span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}` +
	`#leoflow-home-link:hover{background:rgba(15,23,42,.95)}` +
	`.dark #leoflow-home-link{color:#0f172a;background:rgba(241,245,249,.92)}` +
	`.dark #leoflow-home-link:hover{background:#fff}</style>`

// homeLinkHTML renders the operator home link. Both values are escaped, so
// config reaches the page as text and never as markup; the URL's scheme is
// limited to http(s) by config validation.
func homeLinkHTML(label, href string) string {
	return `<a id="leoflow-home-link" href="` + html.EscapeString(href) + `">` +
		`<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" ` +
		`stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">` +
		`<polyline points="15 18 9 12 15 6"></polyline></svg>` +
		`<span>` + html.EscapeString(label) + `</span></a>` + homeLinkStyle
}

// Server serves the embedded Airflow 3.2.1 SPA: static assets under a prefix and
// an index.html fallback for client-side routes.
type Server struct {
	fsys         fs.FS
	static       *staticCache
	version      string
	liteBanner   bool
	proBanner    bool
	editorButton bool
	instanceName string
	homeLabel    string
	homeURL      string
	favicon      string
	stylesheets  []string
}

// stockFavicon is the favicon tag of the pinned Airflow bundle, the anchor
// SetFavicon rewrites. TestEmbeddedBundleFaviconIsRebranded fails if a bundle
// upgrade changes it.
const stockFavicon = `<link rel="icon" type="image/png" href="./static/pin_32.png" />`

// SetLiteBanner toggles injection of the LITE overlay into the served shell. It
// is enabled by the Lite edition (`dexaflow lite`); the demo and production never
// set it.
func (s *Server) SetLiteBanner(on bool) { s.liteBanner = on }

// SetProBanner toggles injection of the gold PRO overlay into the served shell.
// It is enabled by the Pro edition (Helm install); Lite and Demo never set it.
// The two edition pills are mutually exclusive in practice, but the server does
// not enforce that — it trusts the edition resolved upstream.
func (s *Server) SetProBanner(on bool) { s.proBanner = on }

// SetEditorButton toggles injection of the "IDE" button that opens the Lite web
// editor (/ide). It is enabled only when a workspace is configured (Lite).
func (s *Server) SetEditorButton(on bool) { s.editorButton = on }

// SetInstanceName overrides the value used to rewrite the embedded SPA's
// `<title>` tag (issue #D15). Empty falls back to "Dexaflow" so the browser
// tab never shows the upstream "Airflow" string from the bundled fork.
func (s *Server) SetInstanceName(name string) { s.instanceName = name }

// SetHomeLink sets the operator's link back to their platform (#1290), shown on
// every UI page and opened in the same tab. An empty url disables it.
func (s *Server) SetHomeLink(label, url string) { s.homeLabel, s.homeURL = label, url }

// SetFavicon replaces the bundle's favicon with url (#1289). Empty keeps the
// stock icon. Config validation limits url to http(s) or a root-relative path.
func (s *Server) SetFavicon(url string) { s.favicon = url }

// SetStylesheets adds stylesheets every UI page loads in <head>, typically the
// web fonts a theme names (#1289). Config validation limits each URL as for
// SetFavicon.
func (s *Server) SetStylesheets(urls []string) { s.stylesheets = urls }

// New builds a Server over the embedded, pinned SPA bundle.
func New() *Server { return NewFromFS(Assets(), Version()) }

// NewFromFS builds a Server over an arbitrary asset filesystem, so tests can
// inject a fixture instead of the embedded bundle.
func NewFromFS(fsys fs.FS, version string) *Server {
	return &Server{fsys: fsys, version: version, static: &staticCache{fsys: fsys}}
}

// Version returns the pinned upstream Airflow tag the bundle was built from.
func (s *Server) Version() string { return s.version }

// StaticHandler serves the bundle as static files from the embedded FS. The
// caller mounts it with the /static prefix already stripped. Content-hashed
// chunks (under assets/) are marked immutable; index.html is never cached;
// everything else gets a short cache. Compressible assets are gzipped when the
// client accepts it. Missing files yield 404 (no SPA fallback here); directories
// are not listed.
//
// Each file is read, hashed and gzipped once (by Precompress, or else on its
// first request) and served from memory afterwards with a strong ETag, so a
// conditional request gets a 304 and a cold one costs a copy instead of a fresh
// compression.
func (s *Server) StaticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(r.URL.Path, "/")), "/")
		if name == "" {
			name = "index.html"
		}
		entry, err := s.static.lookup(name)
		if err != nil {
			// /static is public, so this line is reachable by anonymous
			// clients: keep it at DEBUG and leave out request headers, which
			// an attacker controls (#506).
			slog.Debug("ui static 404",
				"resolved_name", name,
				"raw_path", r.URL.Path,
			)
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Cache-Control", cacheControl(r.URL.Path))
		h.Set("Content-Type", entry.contentType)
		if entry.gzip != nil {
			h.Add("Vary", "Accept-Encoding")
			if acceptsGzip(r) {
				h.Set("Content-Encoding", "gzip")
				h.Set("ETag", entry.gzipETag)
				// The payload is the pinned, compile-time-embedded SPA bundle served
				// with an explicit Content-Type: a trusted static asset, not user input.
				http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(entry.gzip))
				return
			}
		}
		h.Set("ETag", entry.etag)
		s.serveIdentity(w, r, name)
	})
}

// serveIdentity streams a file uncompressed straight from the asset filesystem.
// The embedded bundle already lives in the binary, so no heap copy is kept for
// it; an embedded file is an io.ReadSeeker, which gives Range support.
func (s *Server) serveIdentity(w http.ResponseWriter, r *http.Request, name string) {
	f, err := s.fsys.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			slog.Debug("ui static close failed", "err", cerr)
		}
	}()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		data, rerr := io.ReadAll(f)
		if rerr != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		rs = bytes.NewReader(data)
	}
	http.ServeContent(w, r, name, time.Time{}, rs)
}

// Precompress reads and gzips every compressible file of the bundle up front, so
// the first browser after a restart does not pay the compression. It is meant to
// run in its own goroutine at startup; a request that arrives first for a file
// waits for that file's build instead of compressing it a second time.
func (s *Server) Precompress() {
	// An unreadable entry is skipped: a request for it still gets its 404.
	walkErr := fs.WalkDir(s.fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !compressible(name) {
			return nil //nolint:nilerr // skip the entry, keep walking.
		}
		if _, lerr := s.static.lookup(name); lerr != nil {
			slog.Debug("ui static precompress skipped a file", "name", name, "err", lerr)
		}
		return nil
	})
	if walkErr != nil {
		slog.Debug("ui static precompress stopped", "err", walkErr)
	}
}

// staticCache holds the served form of every static file built so far. A name
// whose read fails is removed again, so unknown paths cannot grow the cache and
// its size is bounded by the gzipped bundle (about 3 MB for the pinned Airflow
// UI): the raw bytes are not kept, identity is streamed from the bundle.
type staticCache struct {
	fsys    fs.FS
	entries sync.Map // name -> *staticEntry
}

// staticEntry is one file ready to serve: its type, the gzip encoding when the
// type is compressible, and a strong ETag per encoding. once guards the build so
// concurrent first requests compress the file a single time.
type staticEntry struct {
	once        sync.Once
	err         error
	contentType string
	etag        string
	gzip        []byte
	gzipETag    string
}

// lookup returns the entry for name, building it on first use.
func (c *staticCache) lookup(name string) (*staticEntry, error) {
	v, _ := c.entries.LoadOrStore(name, &staticEntry{})
	e, ok := v.(*staticEntry)
	if !ok {
		return nil, fs.ErrInvalid // unreachable: only *staticEntry is stored.
	}
	e.once.Do(func() { e.build(c.fsys, name) })
	if e.err != nil {
		c.entries.CompareAndDelete(name, e)
		return nil, e.err
	}
	return e, nil
}

// build reads the file, hashes it and, for a compressible type, gzips it at the
// best compression level. That costs once what the old path paid on every
// request. A compression error leaves the file served as identity only.
func (e *staticEntry) build(fsys fs.FS, name string) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		e.err = err
		return
	}
	sum := sha256.Sum256(data)
	tag := hex.EncodeToString(sum[:16])
	e.contentType = contentType(name, data)
	e.etag = `"` + tag + `"`
	if compressible(name) {
		if gz, gerr := gzipBytes(data); gerr == nil {
			e.gzip, e.gzipETag = gz, `"`+tag+`-gzip"`
		}
	}
}

// contentType resolves a response Content-Type, forcing application/wasm (which
// Go's MIME table omits, and browsers require for streaming instantiation) and
// sniffing only as a last resort.
func contentType(name string, data []byte) string {
	if strings.HasSuffix(name, ".wasm") {
		return "application/wasm"
	}
	if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
		return ct
	}
	return http.DetectContentType(data)
}

// acceptsGzip reports whether the client advertised gzip support.
func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

// compressibleExts are asset types worth gzipping; already-compressed binaries
// (png, woff2) are skipped.
var compressibleExts = map[string]bool{
	".js": true, ".css": true, ".json": true, ".html": true, ".svg": true,
	".wasm": true, ".map": true, ".txt": true, ".ttf": true,
}

func compressible(name string) bool {
	return compressibleExts[strings.ToLower(filepath.Ext(name))]
}

// gzipBytes compresses data at gzip.BestCompression.
func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := gz.Write(data); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	// Clone so the cache holds exactly the compressed bytes, not the slack the
	// buffer grew while writing.
	return bytes.Clone(buf.Bytes()), nil
}

// Index writes the SPA shell with <base href> set to basePath, so the bundled
// React router resolves routes and asset URLs against the deployment root. It
// is the fallback for any non-static, non-API path. basePath defaults to "/".
func (s *Server) Index(w http.ResponseWriter, basePath string) {
	if basePath == "" {
		basePath = "/"
	}
	data, err := fs.ReadFile(s.fsys, "index.html")
	if err != nil {
		http.Error(w, "UI bundle missing index.html", http.StatusInternalServerError)
		return
	}
	body := strings.ReplaceAll(string(data), baseHrefPlaceholder, basePath)
	// The bundle's main <script src> is rewritten to ./static/assets/, but its
	// modulepreload hints keep a bare ./assets/ prefix that we do not serve under
	// (it collides with the SPA's own /assets route). Point those preloads at the
	// served /static/assets path so they resolve to JS instead of the index.html
	// SPA fallback (a text/html MIME type that breaks module preloading).
	body = strings.ReplaceAll(body, `"./assets/`, `"./static/assets/`)
	// Rewrite the bundled "<title>Airflow</title>" to the configured instance
	// name (issue #D15) so the browser tab brands as Dexaflow on first touch.
	// Empty falls back to "Dexaflow".
	title := s.instanceName
	if title == "" {
		title = "Dexaflow"
	}
	body = strings.ReplaceAll(body, "<title>Airflow</title>", "<title>"+title+"</title>")
	body = s.brand(body)
	// Always inject the clipboard polyfill — no-op on https / localhost, the
	// only place it matters is plain http://<lan-ip>:port (#242).
	body = injectBeforeBodyEnd(body, clipboardFallbackHTML)
	if s.liteBanner {
		body = injectBeforeBodyEnd(body, liteBannerHTML)
	}
	if s.proBanner {
		body = injectBeforeBodyEnd(body, proBannerHTML)
	}
	if s.editorButton {
		body = injectBeforeBodyEnd(body, ideButtonHTML)
	}
	if s.homeURL != "" {
		body = injectBeforeBodyEnd(body, homeLinkHTML(s.homeLabel, s.homeURL))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(body)); err != nil {
		return // client hung up mid-write; nothing actionable to do.
	}
}

// brand applies the favicon and stylesheet settings to the shell. Every value
// is HTML-escaped into its attribute.
func (s *Server) brand(body string) string {
	if s.favicon != "" {
		body = strings.Replace(body, stockFavicon,
			`<link rel="icon" href="`+html.EscapeString(s.favicon)+`" />`, 1)
	}
	if len(s.stylesheets) == 0 {
		return body
	}
	var links strings.Builder
	for _, href := range s.stylesheets {
		links.WriteString(`<link rel="stylesheet" href="` + html.EscapeString(href) + `">`)
	}
	if i := strings.Index(body, "</head>"); i >= 0 {
		return body[:i] + links.String() + body[i:]
	}
	return links.String() + body
}

// injectBeforeBodyEnd places snippet just before </body> so it renders over the
// SPA; if there is no </body> it appends to the end.
func injectBeforeBodyEnd(body, snippet string) string {
	if i := strings.LastIndex(body, "</body>"); i >= 0 {
		return body[:i] + snippet + body[i:]
	}
	return body + snippet
}

// cacheControl picks a Cache-Control value for a static path. Content-hashed
// chunks may be cached forever; the HTML shell never; other files briefly.
func cacheControl(urlPath string) string {
	trimmed := strings.TrimPrefix(urlPath, "/")
	switch {
	case trimmed == "index.html" || trimmed == "":
		return "no-cache"
	case strings.HasPrefix(trimmed, "assets/"):
		return "public, max-age=31536000, immutable"
	default:
		return "public, max-age=3600"
	}
}
