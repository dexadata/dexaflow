package domain

import (
	"reflect"
	"testing"
)

// TestApplyDefaults_AllZeroValuesGetSchemaDefaults verifies that a fully zero
// LeoflowConfig is filled with every default declared in the JSON Schema
// (internal/domain/schemas/leoflow-yaml-schema.json). This is the single
// source of truth for "what does Dexaflow assume when dexaflow.yaml is empty"
// and replaces the scattered inline `if x == "" { x = ...}` fallbacks.
//
// Reason for centralization (user ask 2026-06-01): the multi-DAG workspace
// design lets subdirs ship without a dexaflow.yaml — they MUST still receive
// the same defaults, and "which value did we use?" has to be debuggable.
func TestApplyDefaults_AllZeroValuesGetSchemaDefaults(t *testing.T) {
	c := &LeoflowConfig{}
	c.ApplyDefaults()

	if c.SchemaVersion != "1.0" {
		t.Errorf("SchemaVersion: got %q, want %q", c.SchemaVersion, "1.0")
	}
	if c.PythonVersion != "3.11" {
		t.Errorf("PythonVersion: got %q, want %q", c.PythonVersion, "3.11")
	}
	if c.DagSource != "dag.py" {
		t.Errorf("DagSource: got %q, want %q", c.DagSource, "dag.py")
	}
	wantInclude := []string{"."}
	if !reflect.DeepEqual(c.IncludePaths, wantInclude) {
		t.Errorf("IncludePaths: got %v, want %v", c.IncludePaths, wantInclude)
	}
	wantExclude := []string{".git", "__pycache__", "*.pyc", ".venv", "venv"}
	if !reflect.DeepEqual(c.ExcludePaths, wantExclude) {
		t.Errorf("ExcludePaths: got %v, want %v", c.ExcludePaths, wantExclude)
	}
	if c.Build == nil {
		t.Fatal("Build: nil, want non-nil with defaults")
	}
	if c.Build.Context != "." {
		t.Errorf("Build.Context: got %q, want %q", c.Build.Context, ".")
	}
	wantPlatforms := []string{"linux/amd64"}
	if !reflect.DeepEqual(c.Build.Platforms, wantPlatforms) {
		t.Errorf("Build.Platforms: got %v, want %v", c.Build.Platforms, wantPlatforms)
	}
	if c.Registry == nil {
		t.Fatal("Registry: nil, want non-nil with defaults")
	}
	if c.Registry.AuthMethod != "docker_config" {
		t.Errorf("Registry.AuthMethod: got %q, want %q", c.Registry.AuthMethod, "docker_config")
	}
	if c.Registry.TagStrategy != "version" {
		t.Errorf("Registry.TagStrategy: got %q, want %q", c.Registry.TagStrategy, "version")
	}
}

// TestApplyDefaults_PreservesUserSetValues guards against accidentally
// clobbering an explicit value. The user's choice always wins; defaults
// only fill genuine zero values.
func TestApplyDefaults_PreservesUserSetValues(t *testing.T) {
	c := &LeoflowConfig{
		SchemaVersion: "1.0",
		DagID:         "my_dag",
		PythonVersion: "3.12",
		DagSource:     "pipelines/main.py",
		IncludePaths:  []string{"src", "lib"},
		ExcludePaths:  []string{"tests"},
		Build:         &BuildConfig{Context: "./build", Platforms: []string{"linux/arm64"}},
		Registry:      &RegistryConfig{AuthMethod: "iam", TagStrategy: "sha"},
	}
	c.ApplyDefaults()

	if c.PythonVersion != "3.12" {
		t.Errorf("PythonVersion overwritten: got %q, want 3.12", c.PythonVersion)
	}
	if c.DagSource != "pipelines/main.py" {
		t.Errorf("DagSource overwritten: got %q", c.DagSource)
	}
	if !reflect.DeepEqual(c.IncludePaths, []string{"src", "lib"}) {
		t.Errorf("IncludePaths overwritten: got %v", c.IncludePaths)
	}
	if !reflect.DeepEqual(c.ExcludePaths, []string{"tests"}) {
		t.Errorf("ExcludePaths overwritten: got %v", c.ExcludePaths)
	}
	if c.Build.Context != "./build" {
		t.Errorf("Build.Context overwritten: got %q", c.Build.Context)
	}
	if !reflect.DeepEqual(c.Build.Platforms, []string{"linux/arm64"}) {
		t.Errorf("Build.Platforms overwritten: got %v", c.Build.Platforms)
	}
	if c.Registry.AuthMethod != "iam" {
		t.Errorf("Registry.AuthMethod overwritten: got %q", c.Registry.AuthMethod)
	}
	if c.Registry.TagStrategy != "sha" {
		t.Errorf("Registry.TagStrategy overwritten: got %q", c.Registry.TagStrategy)
	}
}

// TestApplyDefaults_PartialSubStructGetsRemainingDefaulted covers the case
// where a user sets ONE field in a sub-struct and expects the rest to fill in.
// E.g. a yaml that pins Build.Context but leaves Platforms empty should still
// receive linux/amd64 by default.
func TestApplyDefaults_PartialSubStructGetsRemainingDefaulted(t *testing.T) {
	c := &LeoflowConfig{
		Build:    &BuildConfig{Context: "./custom"},                 // Platforms missing
		Registry: &RegistryConfig{URL: "registry.example.com/team"}, // AuthMethod + TagStrategy missing
	}
	c.ApplyDefaults()

	if c.Build.Context != "./custom" {
		t.Errorf("Build.Context: got %q, want './custom'", c.Build.Context)
	}
	if !reflect.DeepEqual(c.Build.Platforms, []string{"linux/amd64"}) {
		t.Errorf("Build.Platforms: got %v, want [linux/amd64]", c.Build.Platforms)
	}
	if c.Registry.URL != "registry.example.com/team" {
		t.Errorf("Registry.URL: got %q", c.Registry.URL)
	}
	if c.Registry.AuthMethod != "docker_config" {
		t.Errorf("Registry.AuthMethod: got %q, want docker_config", c.Registry.AuthMethod)
	}
	if c.Registry.TagStrategy != "version" {
		t.Errorf("Registry.TagStrategy: got %q, want version", c.Registry.TagStrategy)
	}
}

// TestApplyDefaults_IsIdempotent asserts that running ApplyDefaults twice is
// a no-op after the first call. Defaults are filled once and stable; callers
// that re-apply defaults (e.g. after a watcher reload) get the same result.
func TestApplyDefaults_IsIdempotent(t *testing.T) {
	c := &LeoflowConfig{DagID: "x"}
	c.ApplyDefaults()
	snapshot := *c
	c.ApplyDefaults()
	if !reflect.DeepEqual(snapshot, *c) {
		t.Errorf("ApplyDefaults not idempotent:\n first:  %+v\n second: %+v", snapshot, *c)
	}
}
