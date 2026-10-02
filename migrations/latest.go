package migrations

import (
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
)

// migrationFilePattern matches a golang-migrate up file: NNN_name.up.sql.
// The capture group is the leading version integer (zero-padded or not).
var migrationFilePattern = regexp.MustCompile(`^(\d+)_.*\.up\.sql$`)

// Latest returns the highest migration version embedded in this binary. It is
// the binary's view of "newest schema I know about" — the drift detector in
// `dexaflow lite` (#136) compares it against the running database's
// schema_migrations.version to refuse to start when the DB is ahead. An empty
// embed (no .up.sql files) returns an error rather than 0 so a misconfigured
// build is loud.
func Latest() (uint, error) {
	entries, err := fs.ReadDir(Files, ".")
	if err != nil {
		return 0, fmt.Errorf("reading embedded migrations: %w", err)
	}
	var highest uint
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		m := migrationFilePattern.FindStringSubmatch(e.Name())
		if len(m) != 2 {
			continue
		}
		n, perr := strconv.ParseUint(m[1], 10, 32)
		if perr != nil {
			continue
		}
		if uint(n) > highest {
			highest = uint(n)
		}
	}
	if highest == 0 {
		return 0, fmt.Errorf("no .up.sql migrations found in embedded fs")
	}
	return highest, nil
}
