package build

// Options are the inputs to builder selection.
type Options struct {
	// Tool is the local container tool for the fallback path (--builder).
	Tool string
}

// Select resolves the builder for a build in ResolutionOrder and says why it
// was chosen. Only the local daemon fallback is implemented so far, so every
// build resolves to it; the daemonless and remote paths are added in front of
// it without changing callers.
func Select(o Options) (b Builder, reason string) {
	return LocalDaemon(o), "fallback: no daemonless or remote builder is available yet"
}
