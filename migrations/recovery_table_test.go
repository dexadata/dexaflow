package migrations

import (
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// firstRecoveryRow is the first migration the interrupted-migration table of
// the upgrade guide covers; the files before it shipped before the table did.
const firstRecoveryRow = 27

// TestEveryMigrationHasARecoveryRow keeps the interrupted-migration table in
// website/content/operate/upgrades.md complete (#1619): an operator whose
// migration was cut needs the row for that file to know what to clean up and
// which version to force. 047 shipped without one, so a new migration now
// fails here until its row is written.
func TestEveryMigrationHasARecoveryRow(t *testing.T) {
	doc, err := os.ReadFile("../website/content/operate/upgrades.md")
	if err != nil {
		t.Fatalf("reading the upgrade guide: %v", err)
	}
	covered := map[uint64]bool{}
	inTable := false
	number := regexp.MustCompile(`\b0*(\d{2,3})\b`)
	for _, line := range strings.Split(string(doc), "\n") {
		if strings.HasPrefix(line, "| Interrupted file |") {
			inTable = true
			continue
		}
		if !inTable {
			continue
		}
		if !strings.HasPrefix(line, "|") {
			break
		}
		cells := strings.Split(line, "|")
		if len(cells) < 2 {
			continue
		}
		for _, m := range number.FindAllStringSubmatch(cells[1], -1) {
			v, _ := strconv.ParseUint(m[1], 10, 32) //nolint:errcheck // the pattern matched digits only
			covered[v] = true
		}
	}
	if !inTable {
		t.Fatal("the upgrade guide has no table headed `| Interrupted file |`")
	}

	entries, err := fs.ReadDir(Files, ".")
	if err != nil {
		t.Fatalf("reading embedded migrations: %v", err)
	}
	up := regexp.MustCompile(`^(\d+)_.+\.up\.sql$`)
	for _, e := range entries {
		m := up.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		v, err := strconv.ParseUint(m[1], 10, 32)
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if v >= firstRecoveryRow && !covered[v] {
			t.Errorf("%s has no row in the interrupted-migration table of website/content/operate/upgrades.md: add what an interrupted run leaves behind and which version to force", e.Name())
		}
	}
}
