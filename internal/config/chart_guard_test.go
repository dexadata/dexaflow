package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The guards of #1144. TestDocumentedEnvVarsBind runs from the docs to
// serverDefaults; it cannot see a chart value no template reads, an env var a
// template renders that nothing binds, or a map or slice setting that is
// reachable from neither an env var nor the chart. These three can.

// chartDir is helm/dexaflow, found from this file.
func chartDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "helm", "dexaflow")
}

// chartTemplates is the text of every template of the chart.
func chartTemplates(t *testing.T) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(chartDir(t), "templates", "*"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no chart templates found: %v", err)
	}
	var b strings.Builder
	for _, f := range files {
		raw, rerr := os.ReadFile(f) //nolint:gosec // a path under this repository
		if rerr != nil {
			t.Fatal(rerr)
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	return b.String()
}

// chartEnvNotSettings are the env vars the chart renders that are read with
// os.Getenv rather than bound as a setting, each with what reads it.
var chartEnvNotSettings = map[string]string{
	"LEOFLOW_CONFIG": "the path of the server config file the ConfigMap mounts (LoadServer)",
}

// TestChartEnvVarsBind: every DEXAFLOW_* or LEOFLOW_* env var a template
// renders for the server binds to a registered setting, so no value the chart
// sets is dropped by AutomaticEnv.
func TestChartEnvVarsBind(t *testing.T) {
	nameRe := regexp.MustCompile(`name:\s*"?((?:DEXAFLOW|LEOFLOW)_[A-Z0-9_]+)`)
	bindable := bindableEnvVars()
	seen := 0
	for _, m := range nameRe.FindAllStringSubmatch(chartTemplates(t), -1) {
		env := "LEOFLOW_" + strings.TrimPrefix(strings.TrimPrefix(m[1], "DEXAFLOW_"), "LEOFLOW_")
		seen++
		if _, ok := bindable[env]; ok {
			continue
		}
		if _, ok := chartEnvNotSettings[env]; ok {
			continue
		}
		t.Errorf("the chart renders %s, which binds to no setting in serverDefaults: the value is silently ignored", m[1])
	}
	if seen < 50 {
		t.Fatalf("parsed %d env vars from the chart templates, want the full set: the pattern no longer matches", seen)
	}
}

// chartValuesWithoutConsumer are the values.yaml leaves no template reads,
// each with why that is intended.
var chartValuesWithoutConsumer = map[string]string{
	"split.scheduler.replicaCount": "documented as pinned to 1; the chart ignores it on purpose (ADR 0009)",
}

// valueLeaves returns the dotted path of every leaf of a values document; an
// empty map counts as a leaf.
func valueLeaves(prefix string, node any, out *[]string) {
	m, ok := node.(map[string]any)
	if !ok || len(m) == 0 {
		*out = append(*out, prefix)
		return
	}
	for k, v := range m {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		valueLeaves(p, v, out)
	}
}

// TestChartValuesHaveAConsumer: every leaf of values.yaml is read by a
// template, through its own path or a parent's, so a documented value cannot
// sit in the file with no effect (the dead config.cors.allowedOrigins of
// #1144).
func TestChartValuesHaveAConsumer(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(chartDir(t), "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err = yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	refs := map[string]bool{}
	for _, m := range regexp.MustCompile(`\.Values((?:\.[A-Za-z0-9_]+)+)`).FindAllStringSubmatch(chartTemplates(t), -1) {
		refs[strings.TrimPrefix(m[1], ".")] = true
	}
	var leaves []string
	valueLeaves("", values, &leaves)
	sort.Strings(leaves)
	for _, leaf := range leaves {
		if _, ok := chartValuesWithoutConsumer[leaf]; ok {
			continue
		}
		parts := strings.Split(leaf, ".")
		read := false
		for i := len(parts); i > 0 && !read; i-- {
			read = refs[strings.Join(parts[:i], ".")]
		}
		if !read {
			t.Errorf("values.yaml key %s is read by no template: document it as ignored in chartValuesWithoutConsumer or wire it", leaf)
		}
	}
	for leaf := range chartValuesWithoutConsumer {
		if !contains(leaves, leaf) {
			t.Errorf("chartValuesWithoutConsumer names %s, which values.yaml no longer has", leaf)
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// configFileOnly are the map and slice settings that cannot bind from an env
// var, with the chart value that renders them into the config file the
// ConfigMap mounts.
var configFileOnly = map[string]string{
	"auth.oidc.role_mappings": "auth.oidc.roleMappings",
	"auth.oidc.tenant_claims": "auth.oidc.tenantClaims",
}

// mapSliceLeaves returns the mapstructure path of every map or slice field of
// t, walking nested structs.
func mapSliceLeaves(prefix string, typ reflect.Type, out map[string]reflect.Kind) {
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := strings.Split(f.Tag.Get("mapstructure"), ",")[0]
		yamlTag := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			if yamlTag == "" || yamlTag == "-" {
				if tag == "-" && f.Type.Kind() == reflect.Map {
					// A map decoded from the raw YAML under its snake_case name.
					tag = toSnake(f.Name)
				} else {
					continue
				}
			} else {
				tag = yamlTag
			}
		}
		path := tag
		if prefix != "" {
			path = prefix + "." + tag
		}
		switch f.Type.Kind() {
		case reflect.Struct:
			mapSliceLeaves(path, f.Type, out)
		case reflect.Map, reflect.Slice:
			out[path] = f.Type.Kind()
		default:
		}
	}
}

// toSnake turns RoleMappings into role_mappings.
func toSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// TestMapAndSliceSettingsAreReachable: every map or slice setting of
// ServerConfig either binds from an env var (registered in serverDefaults,
// where viper splits a comma-joined value into a list) or is a config-file
// key the chart renders into the ConfigMap. A new list setting outside both
// fails here instead of at a field report.
func TestMapAndSliceSettingsAreReachable(t *testing.T) {
	leaves := map[string]reflect.Kind{}
	mapSliceLeaves("", reflect.TypeOf(ServerConfig{}), leaves)
	if len(leaves) < 5 {
		t.Fatalf("found %d map or slice settings, want the full set: the walk is broken", len(leaves))
	}
	tpl := chartTemplates(t)
	keys := make([]string, 0, len(leaves))
	for k := range leaves {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := serverDefaults[key]; ok {
			continue
		}
		value, fileOnly := configFileOnly[key]
		if !fileOnly {
			t.Errorf("%s (%s) is neither registered in serverDefaults (env) nor a config-file key the chart renders: an install cannot set it", key, leaves[key])
			continue
		}
		if !strings.Contains(tpl, ".Values."+value) {
			t.Errorf("%s is config-file only, but no template reads the chart value %s to render it", key, value)
		}
	}
	for key := range configFileOnly {
		if _, ok := leaves[key]; !ok {
			t.Errorf("configFileOnly names %s, which is not a map or slice setting of ServerConfig", key)
		}
	}
}
