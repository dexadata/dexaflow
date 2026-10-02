package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWorkspaceDirFromConfig covers both the YAML-resolved path and the
// default fallback the restore command applies when config.yaml is silent.
func TestWorkspaceDirFromConfig(t *testing.T) {
	t.Run("returns the configured workspace when present", func(t *testing.T) {
		got, err := workspaceDirFromConfig([]byte("workspace: /tmp/my-ws\n"))
		if err != nil || got != "/tmp/my-ws" {
			t.Errorf("got=%q err=%v, want /tmp/my-ws", got, err)
		}
	})
	t.Run("missing workspace falls back to the default ~/leoflow-projects path", func(t *testing.T) {
		got, err := workspaceDirFromConfig([]byte("admin_email: a@b\n"))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if !strings.HasSuffix(got, "leoflow-projects") {
			t.Errorf("got=%q, want a path ending in leoflow-projects", got)
		}
	})
	t.Run("empty config also falls back to the default", func(t *testing.T) {
		got, err := workspaceDirFromConfig(nil)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if !strings.HasSuffix(got, "leoflow-projects") {
			t.Errorf("got=%q, want a path ending in leoflow-projects", got)
		}
	})
}

// TestLeoflowHomeHasData mirrors the safety guard. A non-existent or empty
// dir is "no install"; one regular file inside it is.
func TestLeoflowHomeHasData(t *testing.T) {
	if leoflowHomeHasData(filepath.Join(t.TempDir(), "definitely-missing")) {
		t.Error("missing dir reported as having data")
	}
	emptyDir := t.TempDir()
	if leoflowHomeHasData(emptyDir) {
		t.Error("empty dir reported as having data")
	}
	withFile := t.TempDir()
	if err := os.WriteFile(filepath.Join(withFile, "config.yaml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !leoflowHomeHasData(withFile) {
		t.Error("dir with a file reported as empty")
	}
}

// TestDefaultBackupOutputPath_IsDeterministicAndUTC pins the format of the
// default --output filename: `leoflow-backup-<UTC timestamp>.tar.gz` in the
// cwd. The timestamp shape (2006-01-02T150405Z) is the contract — operators
// pipe these filenames into rsync/scp loops and parsers; a regression to
// local time or a different separator would silently break automations.
func TestDefaultBackupOutputPath_IsDeterministicAndUTC(t *testing.T) {
	now := time.Date(2026, 5, 28, 14, 30, 45, 0, time.UTC)
	got := defaultBackupOutputPath(now)
	want := "leoflow-backup-2026-05-28T143045Z.tar.gz"
	// filepath.Join may add a leading "./" on some platforms; check the base.
	if filepath.Base(got) != want {
		t.Errorf("defaultBackupOutputPath(2026-05-28T14:30:45Z) base = %q, want %q",
			filepath.Base(got), want)
	}
}

// TestArchiveRoundTrip is the load-bearing contract for the on-disk format
// (#137). It drives the design of writeBackupArchive and readBackupArchive
// from the consumer's POV: write every artifact a real backup carries
// (manifest, config, setup, dump, nested workspace tree), read it back,
// and assert every field round-trips exactly. The .git exclusion is
// asserted here so the walker cannot regress to leaking VCS history.
func TestArchiveRoundTrip(t *testing.T) {
	leoflowHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(leoflowHome, "config.yaml"),
		[]byte("workspace: /tmp/x\nadmin_email: a@b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leoflowHome, "setup.json"),
		[]byte(`{"workspace":"/tmp/x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "dag.py"),
		[]byte("def task(): pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "subdir"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "subdir", "dexaflow.yaml"),
		[]byte("dag_id: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// .git must be excluded — exclusion is part of the contract, not an
	// implementation detail; a regression would leak commit history users
	// did not push.
	if err := os.MkdirAll(filepath.Join(workspace, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".git", "HEAD"),
		[]byte("ref: master"), 0o600); err != nil {
		t.Fatal(err)
	}

	dumpPath := filepath.Join(t.TempDir(), "dump.sql")
	if err := os.WriteFile(dumpPath, []byte("-- fake dump\nSELECT 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := newBackupManifest("v0.0.1-prealpha.99", 17, "16.4")
	manifestBytes, err := marshalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "backup.tar.gz")
	if werr := writeBackupArchive(out, leoflowHome, workspace, dumpPath, manifestBytes); werr != nil {
		t.Fatalf("writeBackupArchive: %v", werr)
	}

	got, err := readBackupArchive(out)
	if err != nil {
		t.Fatalf("readBackupArchive: %v", err)
	}
	if got.Manifest.LeoflowVersion != "v0.0.1-prealpha.99" || got.Manifest.SchemaVersion != 17 {
		t.Errorf("manifest round-trip: got %+v", got.Manifest)
	}
	if !strings.Contains(string(got.Config), "workspace: /tmp/x") {
		t.Errorf("config not preserved: %q", got.Config)
	}
	if !strings.Contains(string(got.Setup), "/tmp/x") {
		t.Errorf("setup.json not preserved: %q", got.Setup)
	}
	if !strings.Contains(string(got.Dump), "SELECT 1") {
		t.Errorf("dump not preserved: %q", got.Dump)
	}
	if string(got.Workspace["dag.py"]) != "def task(): pass\n" {
		t.Errorf("dag.py not preserved: %q", got.Workspace["dag.py"])
	}
	if string(got.Workspace["subdir/dexaflow.yaml"]) != "dag_id: x\n" {
		t.Errorf("subdir/dexaflow.yaml not preserved: %q", got.Workspace["subdir/dexaflow.yaml"])
	}
	if _, gitLeaked := got.Workspace[".git/HEAD"]; gitLeaked {
		t.Error(".git/HEAD leaked into the archive — exclusion broken")
	}
}

// TestReadBackupArchive_RejectsMissingManifest pins the gate the restore
// command relies on: a tar.gz with no MANIFEST.json (or a zero-valued one)
// is refused so the user gets a clear error rather than half-loading the
// rest of the archive into a broken install.
func TestReadBackupArchive_RejectsMissingManifest(t *testing.T) {
	out := filepath.Join(t.TempDir(), "no-manifest.tar.gz")
	// writeBackupArchive will write an empty MANIFEST when called with
	// manifest=nil; that round-trips to manifest_version=0, which the
	// reader refuses.
	if err := writeBackupArchive(out, t.TempDir(), "", "", nil); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := readBackupArchive(out)
	if err == nil {
		t.Fatal("readBackupArchive accepted an archive with an empty manifest")
	}
	if !strings.Contains(err.Error(), "manifest_version") &&
		!strings.Contains(err.Error(), "MANIFEST") {
		t.Errorf("expected manifest-missing/empty error, got %v", err)
	}
}

// TestRestoreWorkspaceTree_RebuildsNestedFiles covers the inverse of the
// walker (#137): the restore command receives a flat path→bytes map from
// readBackupArchive and must rebuild the workspace tree exactly, creating
// intermediate directories as needed. Regression guard for the common
// shape "dag/<subdir>/dexaflow.yaml" — without the MkdirAll on the file's
// parent, restoring a sub-project would fail with "no such file or
// directory" on the first nested write.
func TestRestoreWorkspaceTree_RebuildsNestedFiles(t *testing.T) {
	dst := t.TempDir()
	files := map[string][]byte{
		"dag.py":                []byte("a"),
		"subdir/dexaflow.yaml":  []byte("b"),
		"deeply/nested/file.go": []byte("c"),
	}
	if err := restoreWorkspaceTree(dst, files); err != nil {
		t.Fatalf("restoreWorkspaceTree: %v", err)
	}
	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil {
			t.Errorf("%s: read err %v", rel, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: got %q, want %q", rel, got, want)
		}
	}
}

// TestIsWorkspaceSkipDir locks in which paths the backup walker excludes —
// a regression here would silently inflate the archive (re-including .venv
// after a refactor) or, worse, fail to exclude .git and leak secrets a
// user committed locally but did not push.
func TestIsWorkspaceSkipDir(t *testing.T) {
	for _, dir := range []string{".git", ".venv", "__pycache__", "node_modules", ".pytest_cache"} {
		if !isWorkspaceSkipDir(dir) {
			t.Errorf("%q should be skipped from the backup", dir)
		}
	}
	for _, dir := range []string{"dags", "src", "subproject"} {
		if isWorkspaceSkipDir(dir) {
			t.Errorf("%q should NOT be skipped (user code)", dir)
		}
	}
}

// TestManifestRoundTrip pins the on-disk format of MANIFEST.json. The restore
// command reads it before unpacking anything else, so the contract must be
// stable: a manifest written by version N must be readable by version N (and,
// ideally, by any version M >= N until manifest_version is bumped).
func TestManifestRoundTrip(t *testing.T) {
	m := newBackupManifest("v0.0.1-prealpha.17", 15, "16.4")
	if m.ManifestVersion != backupManifestVersion {
		t.Errorf("manifest_version = %d, want %d", m.ManifestVersion, backupManifestVersion)
	}
	if m.CreatedAt.IsZero() {
		t.Error("created_at must be stamped, got zero")
	}
	data, err := marshalManifest(m)
	if err != nil {
		t.Fatalf("marshalManifest: %v", err)
	}
	got, err := unmarshalManifest(data)
	if err != nil {
		t.Fatalf("unmarshalManifest: %v", err)
	}
	if got.LeoflowVersion != m.LeoflowVersion || got.SchemaVersion != m.SchemaVersion {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, m)
	}
}

// TestUnmarshalManifest_RejectsPreV1 covers the upgrade-resilience contract:
// if someone hand-rolls an archive without manifest_version (or with 0), the
// restore command refuses with a clear message rather than half-loading
// junk.
func TestUnmarshalManifest_RejectsPreV1(t *testing.T) {
	_, err := unmarshalManifest([]byte(`{"leoflow_version":"x","schema_version":15}`))
	if err == nil || !strings.Contains(err.Error(), "no manifest_version") {
		t.Errorf("expected pre-v1 manifest to be refused, got err=%v", err)
	}
}

// TestUnmarshalManifest_RejectsFutureVersion: an archive from a NEWER binary
// (a future format the current restore does not understand) is refused with
// a clear "upgrade leoflow" hint — the inverse of the schema-drift guard in
// the migration path (#136).
func TestUnmarshalManifest_RejectsFutureVersion(t *testing.T) {
	_, err := unmarshalManifest([]byte(`{"manifest_version":999,"leoflow_version":"x","schema_version":15}`))
	if err == nil || !strings.Contains(err.Error(), "newer than this binary supports") {
		t.Errorf("expected future-version manifest to be refused, got err=%v", err)
	}
}

// TestDecideRestoreSafe is the pure safety decision the restore command
// applies (#137). The CLI invokes the surrounding orchestration; the
// decision itself is unit-testable without touching the filesystem.
func TestDecideRestoreSafe(t *testing.T) {
	tests := []struct {
		name               string
		manifestSchema     uint
		embeddedSchema     uint
		homeAlreadyHasData bool
		force              bool
		wantErr            string
	}{
		{
			name:           "fresh restore on empty home is allowed",
			manifestSchema: 15, embeddedSchema: 15,
			homeAlreadyHasData: false, force: false, wantErr: "",
		},
		{
			name:           "older-schema backup on newer binary is fine (binary's m.Up() applies the rest)",
			manifestSchema: 10, embeddedSchema: 15,
			homeAlreadyHasData: false, force: false, wantErr: "",
		},
		{
			name:           "newer-schema backup on older binary is the drift case — refuse",
			manifestSchema: 18, embeddedSchema: 15,
			homeAlreadyHasData: false, force: false,
			wantErr: "backup was taken on a newer schema",
		},
		{
			name:           "non-empty Lite home without --force is refused (data-loss guard)",
			manifestSchema: 15, embeddedSchema: 15,
			homeAlreadyHasData: true, force: false,
			wantErr: "would overwrite an existing install",
		},
		{
			name:           "non-empty Lite home with --force is allowed (operator opted in)",
			manifestSchema: 15, embeddedSchema: 15,
			homeAlreadyHasData: true, force: true, wantErr: "",
		},
		{
			name:           "drift wins over --force (force does not silence corruption)",
			manifestSchema: 18, embeddedSchema: 15,
			homeAlreadyHasData: true, force: true,
			wantErr: "backup was taken on a newer schema",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := decideRestoreSafe(tc.manifestSchema, tc.embeddedSchema, tc.homeAlreadyHasData, tc.force)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("decideRestoreSafe = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Errorf("decideRestoreSafe = nil, want error containing %q", tc.wantErr)
				return
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("decideRestoreSafe err = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}
