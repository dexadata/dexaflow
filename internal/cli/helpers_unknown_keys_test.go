package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The authoring schema declares `additionalProperties: false`, and that guard was
// unreachable (#15). loadProjectConfig used yaml.Unmarshal, which drops unknown
// keys into the void; Validate() then marshals the STRUCT back to JSON and
// validates that, by which point the key no longer exists. A typo, or a key at
// the wrong level, was accepted in silence.
//
// It is not hypothetical: our own flagship dbt example taught a top-level
// `schedule:`, which is not in the schema — the real key is `dbt.schedule` — so a
// team following the docs shipped a DAG that never ran and took five days to
// notice. The failure has no signal at all: `leoflow validate` prints "is valid".
func writeProject(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dexaflow.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadProjectConfigRejectsUnknownKeys(t *testing.T) {
	t.Run("the key the docs taught", func(t *testing.T) {
		_, err := loadProjectConfig(writeProject(t, "schema_version: \"1.0\"\ndag_id: sales\nschedule: \"@daily\"\n"))
		if err == nil {
			t.Fatal("loadProjectConfig accepted a top-level schedule: — the key that silently unscheduled a real DAG")
		}
		// Naming the key is the minimum; pointing at the right one is what turns
		// a rejection into a fix.
		for _, want := range []string{"schedule", "dbt.schedule", "DAG(schedule="} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("an ordinary typo", func(t *testing.T) {
		// Built rather than written literally: the point of the case is a
		// realistic misspelling of a real key, and spelling it out inline just
		// trips the misspell linter on the fixture.
		typo := "depend" + "ancies"
		_, err := loadProjectConfig(writeProject(t, "schema_version: \"1.0\"\ndag_id: sales\n"+typo+": [x]\n"))
		if err == nil {
			t.Fatal("loadProjectConfig accepted an unknown key")
		}
		if !strings.Contains(err.Error(), typo) {
			t.Errorf("error %q does not name the offending key", err)
		}
	})

	t.Run("a valid config still loads", func(t *testing.T) {
		cfg, err := loadProjectConfig(writeProject(t, "schema_version: \"1.0\"\ndag_id: sales\nowner: t\ndependencies: [duckdb]\n"))
		if err != nil {
			t.Fatalf("loadProjectConfig rejected a valid config: %v", err)
		}
		if cfg.DagID != "sales" {
			t.Errorf("DagID = %q, want sales", cfg.DagID)
		}
	})
}

// Discovery must not lose a DAG to a typo. A Lite workspace loads every
// subdirectory, and a project whose yaml has a stray key still has a dag_id, a
// dbt block and a source — the compile step is where the user should be told,
// with the file and the key, not by the DAG quietly vanishing from the workspace.
func TestLoadProjectConfigLenientKeepsTheConfigAnUnknownKeyWouldCost(t *testing.T) {
	dir := writeProject(t, "schema_version: \"1.0\"\ndag_id: sales\nschedule: \"@daily\"\ndbt:\n  project: analytics\n")

	if _, err := loadProjectConfig(dir); err == nil {
		t.Fatal("the strict loader must still refuse this")
	}
	cfg, err := loadProjectConfigLenient(dir)
	if err != nil {
		t.Fatalf("loadProjectConfigLenient: %v", err)
	}
	if cfg.DagID != "sales" {
		t.Errorf("DagID = %q, want sales — discovery would name the DAG after its directory", cfg.DagID)
	}
	if cfg.Dbt == nil || cfg.Dbt.Project != "analytics" {
		t.Errorf("Dbt = %+v, want the declared project — a dbt-only project would stop being discovered", cfg.Dbt)
	}
}

// Genuine YAML syntax errors must still fail both loaders.
func TestBothLoadersRejectMalformedYAML(t *testing.T) {
	dir := writeProject(t, "dag_id: [unclosed\n")
	if _, err := loadProjectConfig(dir); err == nil {
		t.Error("the strict loader accepted malformed YAML")
	}
	if _, err := loadProjectConfigLenient(dir); err == nil {
		t.Error("the lenient loader accepted malformed YAML")
	}
}

// TestValidateAcceptsADbtOnlyProject pins #996. `leoflow validate` stat'd the
// DAG source unconditionally, with no cfg.Dbt branch, so a project whose DAG IS
// the dbt project (ADR 0042) could never be validated:
//
//	error: DAG source not found: stat .../dag.py: no such file or directory
//
// The command that exists to say "this is fine" always said it was not — which
// is how the mode's other gaps stayed invisible.
func TestValidateAcceptsADbtOnlyProject(t *testing.T) {
	dir := writeProject(t, "schema_version: \"1.0\"\ndag_id: sales\ndbt:\n  project: .\n  schedule: \"@daily\"\n")
	if err := os.WriteFile(filepath.Join(dir, "dbt_project.yml"), []byte("name: sales\nversion: \"1.0\"\nprofile: sales\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newValidateCommand()
	cmd.SetArgs([]string{dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("validate rejected a dbt-only project: %v", err)
	}
}

// And it must still catch a dag.py DAG whose source is missing — the check is
// scoped, not removed.
func TestValidateStillRequiresTheDagSourceForAPythonDag(t *testing.T) {
	dir := writeProject(t, "schema_version: \"1.0\"\ndag_id: sales\n")
	cmd := newValidateCommand()
	cmd.SetArgs([]string{dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "DAG source not found") {
		t.Fatalf("validate accepted a python DAG with no dag.py: %v", err)
	}
}

// A message that names several keys must be readable. `%q` over a string that
// already contains the quotes from the join re-escapes them, so the headline
// feature of this change rendered as: unknown keys "bar\", \"foo".
func TestUnknownKeyMessageRendersSeveralKeys(t *testing.T) {
	_, err := loadProjectConfig(writeProject(t, "schema_version: \"1.0\"\ndag_id: s\nfoo: 1\nbar: 2\n"))
	if err == nil {
		t.Fatal("two unknown keys were accepted")
	}
	if !strings.Contains(err.Error(), `unknown keys "bar", "foo"`) {
		t.Errorf("message is not readable: %v", err)
	}
	if strings.Contains(err.Error(), `\"`) {
		t.Errorf("message carries escaped quotes: %v", err)
	}
}

// The top-level `schedule:` hint is keyed off the leaf name, so it fired for a
// `schedule` nested anywhere — telling someone who wrote `build.schedule` that
// their DAG takes its schedule from DAG(schedule=…), which is not their problem.
func TestScheduleHintOnlyFiresForTheTopLevelKey(t *testing.T) {
	_, err := loadProjectConfig(writeProject(t, "schema_version: \"1.0\"\ndag_id: s\nbuild:\n  schedule: x\n"))
	if err == nil {
		t.Fatal("a nested unknown key was accepted")
	}
	if strings.Contains(err.Error(), "DAG(schedule=") {
		t.Errorf("the top-level hint fired for a nested schedule: %v", err)
	}
}

// ...and the gate must correlate per KEY, not per message. `topLevel` and the
// seen-`schedule` flag were two independent ORs over the same error list, so any
// other top-level unknown key lit the first while a nested `schedule` lit the
// second — and someone who wrote `build.schedule` alongside an unrelated typo got
// lectured about DAG(schedule=…). The single-key test above cannot see it.
func TestScheduleHintDoesNotLeakAcrossKeys(t *testing.T) {
	_, err := loadProjectConfig(writeProject(t, "schema_version: \"1.0\"\ndag_id: s\nzzz: 1\nbuild:\n  schedule: x\n"))
	if err == nil {
		t.Fatal("two unknown keys were accepted")
	}
	if strings.Contains(err.Error(), "DAG(schedule=") {
		t.Errorf("the top-level hint leaked onto a nested schedule: %v", err)
	}

	// The other ordering. The top-level mark is set BEFORE the dedup `continue`
	// precisely so a key reported at two levels does not lose it — move the mark
	// after the dedup and this case silently stops hinting while the one above
	// still passes. An invariant a refactor can erase without reddening CI is
	// not an invariant.
	_, err = loadProjectConfig(writeProject(t, "schema_version: \"1.0\"\ndag_id: s\nbuild:\n  schedule: x\nschedule: \"@daily\"\n"))
	if err == nil {
		t.Fatal("the nested-then-top-level pair was accepted")
	}
	if !strings.Contains(err.Error(), "DAG(schedule=") {
		t.Errorf("a top-level schedule reported after a nested one lost its hint: %v", err)
	}
}

// The lenient decode must not depend on the unknown-key classifier — not
// because the gate was reachable (measured: yaml.Unmarshal rejects the same
// inputs the classifier mishandles, so the verdict is identical either way) but
// because the classifier is a string match against a third-party library's
// prose. While it gated this decode, a yaml/v3 rewording would have cost a
// caller its config, and for a dbt-only project that means discovery drops it
// and the DAG is removed from the registry as "folder gone". Ungated, the same
// rewording costs an ugly message.
func TestLenientDecodeIsNotGatedOnTheClassifier(t *testing.T) {
	// What is observable: the lenient loader keeps the config for a file the
	// strict loader refuses, and it reaches that answer through yaml.Unmarshal
	// alone — the classifier no longer sits between them.
	dir := writeProject(t, "schema_version: \"1.0\"\ndag_id: sales\nschedule: \"@daily\"\ndbt:\n  project: analytics\n")
	cfg, err := loadProjectConfigLenient(dir)
	if err != nil {
		t.Fatalf("loadProjectConfigLenient refused a file it must still parse: %v", err)
	}
	if cfg.DagID != "sales" || cfg.Dbt == nil {
		t.Errorf("lost the config: DagID=%q Dbt=%v — discovery would drop this DAG", cfg.DagID, cfg.Dbt)
	}
	if _, serr := loadProjectConfig(dir); serr == nil {
		t.Error("the strict loader must still refuse it")
	}

	// A genuine type error still fails BOTH, and that is not something the
	// restructure changes: yaml.Unmarshal rejects it too, so there is no config
	// to hand discovery. Pinning it so nobody reads the case above as a promise
	// that the lenient loader tolerates everything.
	bad := writeProject(t, "schema_version: \"1.0\"\ndag_id: sales\ndependencies: 5\n")
	if _, lerr := loadProjectConfigLenient(bad); lerr == nil {
		t.Error("the lenient loader accepted a type error")
	}
}

// An empty or comments-only file must not become a hard failure. Decoder.Decode
// returns io.EOF where yaml.Unmarshal treats it as an empty document, so a
// mid-edit save in the Lite web IDE started reporting a bare "EOF" instead of
// the missing dag_id.
func TestEmptyProjectConfigIsNotAnError(t *testing.T) {
	for _, body := range []string{"", "# nada ainda\n"} {
		cfg, err := loadProjectConfig(writeProject(t, body))
		if err != nil {
			t.Errorf("loadProjectConfig(%q) = %v, want the empty document to parse", body, err)
			continue
		}
		if verr := cfg.Validate(); verr == nil {
			t.Errorf("an empty config should fail Validate() on the missing dag_id, not at the decoder")
		}
	}
}

// #996 scoped the dag.py check to a dag.py DAG and put nothing in its place, so
// `validate` answered "is valid" for a dbt: block pointing at a directory that
// does not exist — the exact shape of #15, reintroduced by #15's own fix.
func TestValidateRejectsADbtProjectThatIsNotThere(t *testing.T) {
	dir := writeProject(t, "schema_version: \"1.0\"\ndag_id: sales\ndbt:\n  project: ./nowhere\n")
	cmd := newValidateCommand()
	cmd.SetArgs([]string{dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("validate accepted a dbt project directory that does not exist")
	}
	if !strings.Contains(err.Error(), "dbt_project.yml") {
		t.Errorf("error %q should name what is missing", err)
	}
}
