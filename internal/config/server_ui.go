package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// UISection configures the embedded Airflow UI.
type UISection struct {
	// InstanceName is shown in the UI navbar (Airflow's instance_name). Empty
	// falls back to "Dexaflow"; `dexaflow lite` sets it to mark the environment.
	InstanceName string `mapstructure:"instance_name"`
	// AutoRefreshIntervalSeconds is the SPA's polling cadence for DAG /
	// DagRun / task-instance state refresh (Airflow's auto_refresh_interval).
	// Zero (the default) falls back to api.DefaultUIAutoRefreshIntervalSeconds
	// (5s; 30s before v0.6.0). `dexaflow lite` sets it to 1s for a snappy inner
	// loop so the SPA reflects state changes almost immediately during dev.
	AutoRefreshIntervalSeconds int `mapstructure:"auto_refresh_interval_seconds"`
	// Edition marks the running edition (ADR 0027): EditionCluster for the Helm
	// chart, EditionLite for the binary; EditionProAlias is the earlier name of
	// EditionCluster and means the same. Lite shows the silver LITE badge and
	// cluster the gold CLUSTER badge (independent of the auth mode). Empty or
	// any other value shows no badge; Demo intentionally renders without one.
	Edition string `mapstructure:"edition"`
	// Workspace is the DAG project directory the Lite web editor edits (ADR 0025).
	// Empty disables the editor (Production, or Lite without one).
	Workspace string `mapstructure:"workspace"`
	// MonacoDir is where the pinned Monaco bundle was fetched by `dexaflow setup`;
	// the editor page is served Monaco from it. Empty shows a setup hint.
	MonacoDir string `mapstructure:"monaco_dir"`
	// HomeLink is an optional, persistent link from the UI back to the platform
	// the operator serves Dexaflow from (#1290). Empty shows no link.
	HomeLink HomeLinkSection `mapstructure:"home_link"`
	// Theme is a JSON object in the shape of Airflow's `[api] theme` (#1289):
	// `tokens` (Chakra design tokens, such as colors.brand and fonts),
	// `globalCss`, `icon` and `icon_dark_mode`. The UI applies it through its
	// own theming. Empty keeps the stock look.
	Theme string `mapstructure:"theme"`
	// FaviconURL replaces the UI's favicon. It must be http(s) or root-relative.
	FaviconURL string `mapstructure:"favicon_url"`
	// StylesheetURLs are extra stylesheets loaded by every UI page, typically
	// the web fonts a theme's fonts tokens name. Each must be http(s) or
	// root-relative.
	StylesheetURLs []string `mapstructure:"stylesheet_urls"`
	// ETagRevalidation lets the browser revalidate the UI routes that compute
	// an ETag (the grid's task summaries) with "private, no-cache" instead of
	// no-store, so an unchanged grid poll is answered 304. The browser then
	// keeps the last grid body in its private cache after logout, revalidated
	// before any use. Off by default (ADR 0062 gate): every UI route keeps
	// no-store.
	ETagRevalidation bool `mapstructure:"etag_revalidation"`
}

// HomeLinkSection is the operator's way back from the UI: a label and the
// absolute http(s) URL it opens, in the same tab. Both are set or neither.
type HomeLinkSection struct {
	// Label is the link text, for example the operator's portal name.
	Label string `mapstructure:"label"`
	// URL is where the link goes. It must be an absolute http:// or https:// URL.
	URL string `mapstructure:"url"`
}

// validateHomeLink checks ui.home_link (#1290): both fields or neither, and an
// absolute http(s) URL with a host, so a typo fails boot instead of rendering a
// dead link and no other scheme (javascript:, data:) can reach the page.
func validateHomeLink(l HomeLinkSection) error {
	if l == (HomeLinkSection{}) {
		return nil
	}
	if l.URL == "" {
		return errors.New("ui.home_link.url is required when ui.home_link.label is set")
	}
	if strings.TrimSpace(l.Label) == "" {
		return errors.New("ui.home_link.label is required when ui.home_link.url is set")
	}
	u, err := url.Parse(l.URL)
	if err != nil {
		return fmt.Errorf("ui.home_link.url must be a valid URL (got %q): %w", l.URL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("ui.home_link.url must start with http:// or https:// (got %q)", l.URL)
	}
	if u.Host == "" {
		return fmt.Errorf("ui.home_link.url must include a host (got %q)", l.URL)
	}
	return nil
}

// themeKeys are the top-level keys of a theme the Airflow 3.2.1 UI reads.
var themeKeys = map[string]bool{"tokens": true, "globalCss": true, "icon": true, "icon_dark_mode": true}

// validateBranding checks the #1289 settings: ui.theme is a JSON object with
// only the keys the UI reads (an unknown key is almost always a typo the UI
// would ignore in silence), and every URL, the theme's icons included, is
// http(s) or root-relative.
func validateBranding(u UISection) error {
	if u.Theme != "" {
		var theme map[string]json.RawMessage
		if err := json.Unmarshal([]byte(u.Theme), &theme); err != nil {
			return fmt.Errorf("ui.theme must be a JSON object: %w", err)
		}
		for key, raw := range theme {
			if !themeKeys[key] {
				return fmt.Errorf("ui.theme has unknown key %q (the UI reads tokens, globalCss, icon, icon_dark_mode)", key)
			}
			if key != "icon" && key != "icon_dark_mode" {
				continue
			}
			var icon string
			if err := json.Unmarshal(raw, &icon); err != nil {
				return fmt.Errorf("ui.theme icon %q must be a string URL", key)
			}
			if err := checkAssetURL(icon); err != nil {
				return fmt.Errorf("ui.theme icon %q: %w", key, err)
			}
		}
	}
	if u.FaviconURL != "" {
		if err := checkAssetURL(u.FaviconURL); err != nil {
			return fmt.Errorf("ui.favicon_url: %w", err)
		}
	}
	for _, sheet := range u.StylesheetURLs {
		if err := checkAssetURL(sheet); err != nil {
			return fmt.Errorf("ui.stylesheet_urls: %w", err)
		}
	}
	return nil
}

// checkAssetURL accepts an absolute http(s) URL with a host or a root-relative
// path ("/brand/logo.svg"). It refuses every other scheme and the
// protocol-relative "//host" form, which would load from a host nobody named.
func checkAssetURL(raw string) error {
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%q is not a valid URL: %w", raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q must be an http(s) URL or a root-relative path", raw)
	}
	return nil
}
