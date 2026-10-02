package domain

import (
	"errors"
	"strings"
	"testing"
)

func dbtCfg(project string) *LeoflowConfig {
	return &LeoflowConfig{
		SchemaVersion: "1.0",
		DagID:         "sales",
		Dbt:           &DbtConfig{Project: project, Schedule: "@daily"},
	}
}

// dbt.project is resolved with filepath.Join(dagDir, project) and, for a Pro
// image build, baked into the image at that relative path. Both break for a path
// that is absolute or leaves the DAG directory — and they break silently, which
// is the part worth catching.
func TestValidateRejectsUnusableDbtProject(t *testing.T) {
	for _, tc := range []struct {
		name    string
		project string
		wants   string
	}{
		// filepath.Join("/dags/sales", "/opt/dbt/proj") is
		// "/dags/sales/opt/dbt/proj" — Join does not treat an absolute second
		// element specially, so the leading slash is simply swallowed.
		{"absolute", "/opt/dbt/proj", "absolute"},
		{"absolute root", "/", "absolute"},
		// filepath.Join("/dags/sales", "../../../etc") is "/etc": outside the DAG
		// directory, so outside the Docker build context too.
		{"escapes the dag directory", "../../../etc", "outside"},
		{"escapes one level", "../sibling", "outside"},
		{"escapes after descending", "transform/../../elsewhere", "outside"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := dbtCfg(tc.project).Validate()
			if err == nil {
				t.Fatalf("Validate accepted dbt.project = %q", tc.project)
			}
			if !errors.Is(err, ErrInvalidDbtProject) {
				t.Fatalf("error does not wrap ErrInvalidDbtProject: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error %q should explain the problem (%q)", err, tc.wants)
			}
			if !strings.Contains(err.Error(), tc.project) {
				t.Errorf("error %q should quote the offending value", err)
			}
		})
	}
}

// The ordinary forms must keep working, including the ones a dbt user writes.
func TestValidateAcceptsUsableDbtProject(t *testing.T) {
	for _, project := range []string{
		"",                       // no dbt project declared
		".",                      // the DAG directory itself
		"transform",              // the common case
		"./transform",            // the same, written explicitly
		"dbt/transform",          // nested
		"transform/../analytics", // normalises to "analytics", still inside
	} {
		t.Run("project="+project, func(t *testing.T) {
			if err := dbtCfg(project).Validate(); err != nil {
				t.Errorf("rejected a usable dbt.project %q: %v", project, err)
			}
		})
	}
}

// dbt.manifest goes through the same filepath.Join as project
// (dbt_manifest.go:26 joins it onto the already-joined project dir), so an
// absolute manifest path is mangled the same way and needs the same rejection.
// The first version of this validation checked only project, which left half the
// reported bug in place.
func TestValidateRejectsUnusableDbtManifest(t *testing.T) {
	for _, tc := range []struct{ name, manifest, wants string }{
		{"absolute", "/tmp/proj/target/manifest.json", "absolute"},
		{"escapes", "../../../etc/passwd", "outside"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := dbtCfg("transform")
			cfg.Dbt.Manifest = tc.manifest
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate accepted dbt.manifest = %q", tc.manifest)
			}
			if !errors.Is(err, ErrInvalidDbtProject) {
				t.Fatalf("error does not wrap ErrInvalidDbtProject: %v", err)
			}
			if !strings.Contains(err.Error(), "manifest") {
				t.Errorf("error %q should name the manifest field, not just the project", err)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error %q should explain the problem (%q)", err, tc.wants)
			}
		})
	}
}

func TestValidateAcceptsUsableDbtManifest(t *testing.T) {
	for _, m := range []string{"", "target/manifest.json", "./target/manifest.json"} {
		cfg := dbtCfg("transform")
		cfg.Dbt.Manifest = m
		if err := cfg.Validate(); err != nil {
			t.Errorf("rejected a usable dbt.manifest %q: %v", m, err)
		}
	}
}

// TestValidateDbtGroupsProjectRejectsEscapingAndAbsolutePaths extends the ADR
// 0042 guard to ADR 0043's task groups. It covered cfg.Dbt only and returned
// early when that was nil — which is every hybrid DAG — so dbt_groups paths fed
// the same filepath.Join chain and the same Docker build context unguarded.
//
// Measured against real builds, the silent one is the ABSOLUTE path, not the
// escaping one: Docker resolves a COPY source against the context root, so
// "/opt/dbt" builds GREEN against <context>/opt/dbt and bakes a directory nobody
// named. Go does the mirror thing on the host — filepath.Join swallows the
// leading slash — which is what the file comment above already describes.
//
// "../shared" is loud but misleading rather than silent: the classic builder
// refuses with "forbidden path outside the build context", and BuildKit clamps
// to <context>/shared, so it is green only when the DAG dir has its own shared/
// and then bakes THAT. Neither ever reaches the sibling — while Lite, which
// resolves filepath.Abs on the host, does. Lite and Pro diverge on which
// directory they read, and no message mentions dexaflow.yaml.
func TestValidateDbtGroupsProjectRejectsEscapingAndAbsolutePaths(t *testing.T) {
	for _, tc := range []struct {
		name  string
		group *DbtConfig
		field string
	}{
		{"escaping project", &DbtConfig{Project: "../shared"}, "dbt_groups.transform.project"},
		{"absolute project", &DbtConfig{Project: "/opt/dbt"}, "dbt_groups.transform.project"},
		{"escaping manifest", &DbtConfig{Project: "t", Manifest: "../x/manifest.json"}, "dbt_groups.transform.manifest"},
		{"absolute manifest", &DbtConfig{Project: "t", Manifest: "/tmp/manifest.json"}, "dbt_groups.transform.manifest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &LeoflowConfig{DbtGroups: map[string]*DbtConfig{"transform": tc.group}}
			err := cfg.validateDbtProject()
			if !errors.Is(err, ErrInvalidDbtProject) {
				t.Fatalf("validateDbtProject() = %v, want ErrInvalidDbtProject", err)
			}
			// The message must name the key, not just the value: a config with
			// several groups is otherwise a hunt.
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error %q does not name %q", err, tc.field)
			}
		})
	}
}

// TestValidateDbtGroupsErrorIsDeterministic: DbtGroups is a map, so reporting
// the first offender found by range order would name a different group on
// different runs — an error message that changes between identical compiles.
func TestValidateDbtGroupsErrorIsDeterministic(t *testing.T) {
	cfg := &LeoflowConfig{DbtGroups: map[string]*DbtConfig{
		"zulu": {Project: "../z"}, "alpha": {Project: "../a"},
		"mike": {Project: "../m"}, "bravo": {Project: "../b"},
	}}
	first := cfg.validateDbtProject()
	if first == nil {
		t.Fatal("validateDbtProject() = nil, want an error")
	}
	for range 50 {
		if got := cfg.validateDbtProject(); got.Error() != first.Error() {
			t.Fatalf("error text is not stable: %q then %q", first, got)
		}
	}
	if !strings.Contains(first.Error(), "dbt_groups.alpha.project") {
		t.Errorf("want the lowest-sorted group reported first, got %q", first)
	}
}

// TestValidateDbtGroupsAcceptsContainedPaths is the bidirectional half: the
// guard must not start refusing the shapes the docs teach.
func TestValidateDbtGroupsAcceptsContainedPaths(t *testing.T) {
	cfg := &LeoflowConfig{DbtGroups: map[string]*DbtConfig{
		"transform": {Project: "./transform"},
		"marketing": {Project: "marketing", Manifest: "target/manifest.json"},
		"root":      {Project: "."},
		"empty":     {},
		"nil":       nil,
	}}
	if err := cfg.validateDbtProject(); err != nil {
		t.Errorf("validateDbtProject() = %v, want nil for contained paths", err)
	}
}
