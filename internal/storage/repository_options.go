package storage

import (
	"errors"

	"github.com/dexadata/dexaflow/internal/secrets"
)

// Option configures a Repository at construction (#1949). Each option documents
// what a Repository built without it does.
type Option func(*repoConfig)

// repoConfig collects the options before they are applied, so a constructor
// can tell an option that was given its zero value from one never given.
type repoConfig struct {
	cipher           secrets.Cipher
	varEncryption    VariableEncryption
	extCoverage      externalSecretCoverage
	pausedAtCreation *bool
	xcomValues       XComValueDeleter
}

// apply copies c onto r. An option never given leaves its field at the zero
// value, which is the documented default for each of them.
func (r *Repository) apply(c repoConfig) {
	r.cipher = c.cipher
	r.varEncryption = c.varEncryption
	r.extCoverage = c.extCoverage
	r.pausedAtCreation = c.pausedAtCreation != nil && *c.pausedAtCreation
	r.xcomValues = c.xcomValues
}

// WithCipher sets the encryption cipher used for connection secrets (ADR 0019)
// and sealed Variable values (#507). Without it, connection writes fail rather
// than storing plaintext.
func WithCipher(c secrets.Cipher) Option {
	return func(cfg *repoConfig) { cfg.cipher = c }
}

// WithVariableEncryption sets how Variable values are written and which way the
// boot sweep converges them. The default is off.
func WithVariableEncryption(m VariableEncryption) Option {
	return func(cfg *repoConfig) { cfg.varEncryption = m }
}

// WithExternalSecretCoverage sets the operator-configured external-backend
// coverage used by the D6 registration check (ADR 0060 B1). Without it, the
// check is the pre-0060 vault-existence check (a declared name must exist in
// the vault). It must be wired from operator/Helm config, never from a DAG.
func WithExternalSecretCoverage(c externalSecretCoverage) Option {
	return func(cfg *repoConfig) { cfg.extCoverage = c }
}

// WithDagsArePausedAtCreation sets whether a DAG registered for the first time
// starts paused when its spec has no is_paused_upon_creation (the server's
// dags_are_paused_at_creation, #1705). It never affects a DAG that exists.
// Without it, new DAGs are registered active.
func WithDagsArePausedAtCreation(paused bool) Option {
	return func(cfg *repoConfig) { cfg.pausedAtCreation = &paused }
}

// WithXComBackend sets the XCom value store, so a clear deletes the values of
// the attempts it clears and not only their index rows (#1131). Without it a
// clear removes only the index rows.
func WithXComBackend(b XComValueDeleter) Option {
	return func(cfg *repoConfig) { cfg.xcomValues = b }
}

// Errors NewServerRepository returns when a dependency the server must decide
// was not given.
var (
	ErrNoXComBackend      = errors.New("storage: server repository needs WithXComBackend")
	ErrNoPausedAtCreation = errors.New("storage: server repository needs WithDagsArePausedAtCreation")
)

// NewServerRepository builds the control plane's Repository. Unlike
// NewRepository it refuses to build one whose missing options would silently
// change behavior: without an XCom backend a clear leaves the values of the
// cleared attempts behind, and without dags_are_paused_at_creation new DAGs
// start active where the server default pauses them. The cipher, Variable
// encryption and external-secret coverage stay optional, because running
// without each is a supported configuration the server already reports.
func NewServerRepository(pg *Postgres, opts ...Option) (*Repository, error) {
	var c repoConfig
	for _, o := range opts {
		o(&c)
	}
	if c.xcomValues == nil {
		return nil, ErrNoXComBackend
	}
	if c.pausedAtCreation == nil {
		return nil, ErrNoPausedAtCreation
	}
	return NewRepository(pg, opts...), nil
}
