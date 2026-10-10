package cli

import (
	"encoding/json"
	"fmt"
	"time"
)

// backupManifestVersion is the on-disk format version of MANIFEST.json. Bumping
// it would let restore tell old bundles from new ones and refuse what it
// cannot parse. v1 ships with the first backup command (#137).
const backupManifestVersion = 1

// backupManifest is the metadata header carried inside every Lite backup
// archive as MANIFEST.json. The restore command reads it before unpacking
// anything else, so it can refuse incompatible bundles loudly rather than
// half-restoring and leaving the user in a broken state. Adding fields is
// safe (omitempty); removing or repurposing them requires bumping
// backupManifestVersion.
type backupManifest struct {
	ManifestVersion int       `json:"manifest_version"`
	LeoflowVersion  string    `json:"leoflow_version"`
	SchemaVersion   uint      `json:"schema_version"`
	PostgresVersion string    `json:"postgres_version,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// newBackupManifest stamps the current binary's view of the world into a
// manifest ready to write into the archive.
func newBackupManifest(leoflowVersion string, schemaVersion uint, postgresVersion string) backupManifest {
	return backupManifest{
		ManifestVersion: backupManifestVersion,
		LeoflowVersion:  leoflowVersion,
		SchemaVersion:   schemaVersion,
		PostgresVersion: postgresVersion,
		CreatedAt:       time.Now().UTC().Truncate(time.Second),
	}
}

// marshalManifest serializes the manifest with indentation for human
// inspection — the file inside the archive is small enough that pretty
// printing wins over compactness, and operators do read it when triaging.
func marshalManifest(m backupManifest) ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// decideRestoreSafe is the pure safety decision the restore command applies
// before unpacking anything. It folds three rules into one place so the CLI
// orchestration stays thin and the contract stays unit-testable:
//
//  1. **Schema drift**: a backup taken on a schema_version *higher* than this
//     binary embeds means the user grabbed an archive from a newer install
//     and is trying to load it into an older one. Restoring would leave the
//     DB with rows the binary cannot read. Refuse loudly, mirror the upgrade
//     drift detector in #136. Force does NOT silence this — corruption is
//     not opt-in.
//  2. **Destructive overwrite**: if ~/.dexaflow already holds an install,
//     restoring would clobber the user's existing data. Refuse unless the
//     operator passed --force, the explicit "I know" override.
//  3. Otherwise, allow.
//
// Older-schema backups (manifest < embedded) are allowed: the restore
// applies the SQL dump as-is and the existing m.Up() catches the DB up to
// the binary on the next start.
func decideRestoreSafe(manifestSchema, embeddedSchema uint, homeAlreadyHasData, force bool) error {
	if manifestSchema > embeddedSchema {
		return fmt.Errorf(
			"backup was taken on a newer schema (version %d) than this binary supports (%d); "+
				"upgrade dexaflow before restoring this archive",
			manifestSchema, embeddedSchema,
		)
	}
	if homeAlreadyHasData && !force {
		return fmt.Errorf(
			"refusing: restore would overwrite an existing install at ~/.dexaflow; " +
				"pass --force to confirm, or move/back-up the existing install first",
		)
	}
	return nil
}

// unmarshalManifest is the read side; an unknown manifest_version is reported
// clearly so the user is told to upgrade rather than chase mysterious errors.
func unmarshalManifest(data []byte) (backupManifest, error) {
	var m backupManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return backupManifest{}, fmt.Errorf("decoding backup manifest: %w", err)
	}
	if m.ManifestVersion == 0 {
		return backupManifest{}, fmt.Errorf("backup manifest has no manifest_version field; archive is corrupt or pre-v1")
	}
	if m.ManifestVersion > backupManifestVersion {
		return backupManifest{}, fmt.Errorf("backup manifest_version %d is newer than this binary supports (%d); upgrade dexaflow",
			m.ManifestVersion, backupManifestVersion)
	}
	return m, nil
}
