package domain

import (
	"strings"
	"testing"
)

func validBuildInfo() BuildInfo {
	return BuildInfo{
		Commit:      "0123456789abcdef0123456789abcdef01234567",
		Repo:        "https://github.com/acme/etl",
		PipelineURL: "https://github.com/acme/etl/actions/runs/42",
		Builder:     "code-layer",
		BaseImage:   "ghcr.io/dexadata/dexaflow-runtime:py3.12-v0.5.4",
		CLIVersion:  "v0.5.4",
	}
}

// TestBuildInfoValidate: build_info is external input, so every field is
// bounded, free of control and format characters, and the URLs are absolute
// http or https without credentials. The error names the field.
func TestBuildInfoValidate(t *testing.T) {
	if err := validBuildInfo().Validate(); err != nil {
		t.Fatalf("valid build info refused: %v", err)
	}
	if err := (BuildInfo{}).Validate(); err != nil {
		t.Fatalf("empty build info refused: %v", err)
	}
	short := validBuildInfo()
	short.Commit = "0123abc"
	if err := short.Validate(); err != nil {
		t.Errorf("short commit refused: %v", err)
	}
	for _, tc := range []struct {
		name, field string
		edit        func(*BuildInfo)
	}{
		{"commit not hex", "commit", func(b *BuildInfo) { b.Commit = "main" }},
		{"commit upper case", "commit", func(b *BuildInfo) { b.Commit = "ABCDEF0" }},
		{"commit too short", "commit", func(b *BuildInfo) { b.Commit = "abc12" }},
		{"commit too long", "commit", func(b *BuildInfo) { b.Commit = strings.Repeat("a", 65) }},
		{"repo ssh", "repo", func(b *BuildInfo) { b.Repo = "git@github.com:acme/etl.git" }},
		{"repo credentials", "repo", func(b *BuildInfo) { b.Repo = "https://x-access-token:secret@github.com/acme/etl" }},
		{"repo too long", "repo", func(b *BuildInfo) { b.Repo = "https://h/" + strings.Repeat("p", 2048) }},
		{"repo javascript", "repo", func(b *BuildInfo) { b.Repo = "javascript:alert(1)" }},
		{"pipeline ftp", "pipeline_url", func(b *BuildInfo) { b.PipelineURL = "ftp://ci.example/1" }},
		{"pipeline relative", "pipeline_url", func(b *BuildInfo) { b.PipelineURL = "/runs/1" }},
		{"pipeline control", "pipeline_url", func(b *BuildInfo) { b.PipelineURL = "https://ci.example/\n1" }},
		{"pipeline query", "pipeline_url", func(b *BuildInfo) { b.PipelineURL = "https://ci.example/1?token=x" }},
		{"pipeline fragment", "pipeline_url", func(b *BuildInfo) { b.PipelineURL = "https://ci.example/1#log" }},
		{"pipeline empty query", "pipeline_url", func(b *BuildInfo) { b.PipelineURL = "https://ci.example/1?" }},
		{"repo query", "repo", func(b *BuildInfo) { b.Repo = "https://github.com/acme/etl?x=1" }},
		{"builder invalid utf8", "builder", func(b *BuildInfo) { b.Builder = "docker\xff" }},
		{"cli version invalid utf8", "cli_version", func(b *BuildInfo) { b.CLIVersion = "v1\xc3" }},
		{"builder control", "builder", func(b *BuildInfo) { b.Builder = "docker\x00" }},
		{"builder bidi", "builder", func(b *BuildInfo) { b.Builder = "docker\u202e" }},
		{"builder too long", "builder", func(b *BuildInfo) { b.Builder = strings.Repeat("b", 256) }},
		{"base image space", "base_image", func(b *BuildInfo) { b.BaseImage = "python 3" }},
		{"base image too long", "base_image", func(b *BuildInfo) { b.BaseImage = strings.Repeat("i", 513) }},
		{"cli version space", "cli_version", func(b *BuildInfo) { b.CLIVersion = "v0.5.4 dirty" }},
		{"cli version too long", "cli_version", func(b *BuildInfo) { b.CLIVersion = strings.Repeat("v", 129) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := validBuildInfo()
			tc.edit(&b)
			err := b.Validate()
			if err == nil {
				t.Fatalf("%+v accepted", b)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error %q does not name %s", err, tc.field)
			}
		})
	}
}

// TestBuildInfoSanitized keeps every valid field and drops each invalid one,
// so a client never sends what the server would refuse.
func TestBuildInfoSanitized(t *testing.T) {
	b := validBuildInfo()
	b.Repo = "git@github.com:acme/etl.git"
	b.Builder = "docker\x07"
	got := b.Sanitized()
	want := validBuildInfo()
	want.Repo, want.Builder = "", ""
	if got != want {
		t.Errorf("Sanitized = %+v, want %+v", got, want)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("sanitized build info refused: %v", err)
	}
	if !(BuildInfo{}).IsZero() || validBuildInfo().IsZero() {
		t.Error("IsZero is wrong")
	}
}
