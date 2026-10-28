package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzParseProjectConfig feeds arbitrary dexaflow.yaml documents through the
// parse every command and the Lite discovery run (parseProjectConfig), then
// the schema validation and the generated-Dockerfile checks. The file is
// author-written and the Lite web IDE saves it mid-edit, so a malformed one
// must come back as an error, never a panic.
func FuzzParseProjectConfig(f *testing.F) {
	seeds, _ := filepath.Glob(filepath.Join("..", "..", "bundle", "dags", "*", "dexaflow.yaml"))
	more, _ := filepath.Glob(filepath.Join("..", "..", "test", "soak", "dags", "*", "dexaflow.yaml"))
	for _, path := range append(seeds, more...) {
		if data, err := os.ReadFile(path); err == nil {
			f.Add(data)
		}
	}
	f.Add([]byte(""))
	f.Add([]byte("# only a comment\n"))
	f.Add([]byte("dag_id: d\ntypo_key: 1\n"))
	f.Add([]byte("dag_id: d\npython_version: \"3.10\"\nconnectors: [postgres, http]\nsystem_packages: [curl]\n" +
		"tasks:\n  t:\n    retries: -1\n    size: 2048\n    resources:\n      limits:\n        memory: 2GB\n"))
	f.Add([]byte("dag_id: d\nbuild:\n  dockerfile: ../Dockerfile\n  base_image: \"python:3.11-slim\\nRUN id\"\n"))
	f.Add([]byte("dag_id: [unclosed\n"))
	f.Add([]byte("a: &a [*a, *a]\nb: *a\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, _, err := parseProjectConfig(data)
		if err != nil {
			if cfg != nil {
				t.Fatalf("parse error %v returned a config", err)
			}
			return
		}
		_ = cfg.Validate()                 //nolint:errcheck // only the absence of a panic is under test here
		_ = cfg.ValidateDockerfileValues() //nolint:errcheck // idem
	})
}
