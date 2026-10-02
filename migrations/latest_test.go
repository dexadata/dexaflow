package migrations

import "testing"

// TestLatestReturnsMaxEmbeddedVersion: Latest must return the highest version
// number among the embedded .up.sql files. The drift detector in `dexaflow lite`
// compares this against the DB's schema_migrations.version to refuse to start
// when the DB is ahead of the binary (#136). A wrong answer here either fails
// to detect drift (data loss risk) or false-positives on a fresh install.
func TestLatestReturnsMaxEmbeddedVersion(t *testing.T) {
	v, err := Latest()
	if err != nil {
		t.Fatalf("Latest err = %v", err)
	}
	if v < 1 {
		t.Fatalf("Latest = %d, want a positive integer (the highest embedded migration)", v)
	}
	// Lower bound sanity: as the codebase grows, this number only goes up.
	// 15 is the count at the time of writing (#128 added 015_ti_heartbeat).
	if v < 15 {
		t.Errorf("Latest = %d, want >= 15 (migrations are added monotonically); did the embed glob regress?", v)
	}
}
