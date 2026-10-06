//go:build crashhooks

package cli

import (
	"os"
	"syscall"
)

// This file is compiled only with `-tags crashhooks`, for the end-to-end
// crash-injection runs of `dexaflow lite migrate-key` against a real binary
// (ADR 0065 section 9). DEXAFLOW_KEY_MIGRATION_CRASH_AT names a step boundary
// (kpAfterPreflight ... kpAfterDrop); the process SIGKILLs itself there. A
// release build has no such hook.
func init() {
	point := os.Getenv("DEXAFLOW_KEY_MIGRATION_CRASH_AT")
	if point == "" {
		return
	}
	migrateKeyCrashHook = func(p string) {
		if p == point {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL) //nolint:errcheck // the next line never runs if it worked
			select {}
		}
	}
}
