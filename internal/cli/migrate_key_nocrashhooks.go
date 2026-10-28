//go:build !crashhooks

package cli

// migrateKeyCrashHook is the hook `dexaflow lite migrate-key` calls at each
// step boundary: a no-op in every build but the crash-injection one (build
// tag crashhooks, migrate_key_crashhooks.go), which SIGKILLs the process at
// a named step.
func migrateKeyCrashHook() func(string) { return func(string) {} }
