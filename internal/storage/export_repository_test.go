package storage

// Configure applies opts to a Repository already built, keeping every option
// not given as it was, so a test can swap a key mid-test the way a restart with
// a rotated key would (#1949). Production configures a Repository only at
// construction.
func (r *Repository) Configure(opts ...Option) {
	paused := r.pausedAtCreation
	c := repoConfig{
		cipher:           r.cipher,
		varEncryption:    r.varEncryption,
		extCoverage:      r.extCoverage,
		pausedAtCreation: &paused,
		xcomValues:       r.xcomValues,
	}
	for _, o := range opts {
		o(&c)
	}
	r.apply(c)
}
