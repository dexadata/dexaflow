package config

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// bindableEnvVars returns the set of LEOFLOW_* environment-variable names that
// LoadServer can bind, derived from serverDefaults. viper's AutomaticEnv only
// binds an env var for a key it has seen via SetDefault (see server.go), so the
// registered defaults are the single source of truth for what binds. The env
// name for a key is LEOFLOW_ + the key uppercased with '.' replaced by '_'.
func bindableEnvVars() map[string]struct{} {
	out := make(map[string]struct{}, len(serverDefaults))
	for key := range serverDefaults {
		env := "LEOFLOW_" + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
		out[env] = struct{}{}
	}
	return out
}

// docEnvVarsFromReference parses the configuration reference and returns every
// LEOFLOW_* variable named in the Variable column of a settings table. Rows that
// describe a config-file/Helm-values-only key spell it in dotted form
// (executor.defaults.staging_size) precisely because its env var does not bind,
// so those are correctly excluded. Only the first table column is scanned, so
// LEOFLOW_* tokens that appear in prose (e.g. LEOFLOW_AGENT_TOKEN, an agent-side
// credential) are not treated as server settings.
func docEnvVarsFromReference(t *testing.T) []string {
	t.Helper()
	// The reference names variables by their current DEXAFLOW_ spelling; the
	// LEOFLOW_ spelling binds to the same key (bindBothPrefixes). Both are
	// normalized to the legacy form bindableEnvVars uses.
	envRe := regexp.MustCompile(`(?:DEXAFLOW|LEOFLOW)_[A-Z0-9_]+`)
	seen := map[string]struct{}{}
	var vars []string
	for _, firstCol := range docSettingFirstColumns(t) {
		for _, v := range envRe.FindAllString(firstCol, -1) {
			v = "LEOFLOW_" + strings.TrimPrefix(strings.TrimPrefix(v, "DEXAFLOW_"), "LEOFLOW_")
			if _, dup := seen[v]; dup {
				continue
			}
			seen[v] = struct{}{}
			vars = append(vars, v)
		}
	}
	if len(vars) == 0 {
		t.Fatal("no DEXAFLOW_* variables parsed from the configuration reference")
	}
	return vars
}

// docSettingFirstColumns returns the first column of every table row in the
// configuration reference — the cell that names the setting, either as its
// LEOFLOW_* env var or, for a config-file/Helm-values-only key, in dotted form.
// It is the shared read side of both directions of the doc/binding guard.
func docSettingFirstColumns(t *testing.T) []string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	docPath := filepath.Join(filepath.Dir(thisFile), "..", "..",
		"website", "content", "reference", "configuration.md")
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}
	var cols []string
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		// First column only: text up to the first column separator.
		firstCol := line
		if i := strings.Index(line[1:], "|"); i >= 0 {
			firstCol = line[:i+1]
		}
		cols = append(cols, firstCol)
	}
	if len(cols) == 0 {
		t.Fatalf("no table rows parsed from %s", docPath)
	}
	return cols
}

// TestDocumentedEnvVarsBind is the class-level guard: every LEOFLOW_* variable
// documented as a server setting must be registered in serverDefaults, otherwise
// AutomaticEnv silently drops it — the failure mode of issue #725, where a Helm
// install (env-only override path) could not set server.trusted_proxies or the
// executor resource defaults.
func TestDocumentedEnvVarsBind(t *testing.T) {
	bindable := bindableEnvVars()
	for _, env := range docEnvVarsFromReference(t) {
		if _, ok := bindable[env]; !ok {
			t.Errorf("%s is documented as a server setting but is not registered "+
				"in serverDefaults, so viper's AutomaticEnv will not bind it "+
				"(Helm/env override silently ignored)", env)
		}
	}
}

// TestLoadServerBindsTrustedProxies proves the []string leaf binds from a single
// comma-separated env var — the only override path a Helm install has, since the
// chart passes no server config file. Consequence when unbound (#725): the login
// limiter keys on the ingress IP for every request, so a handful of bad logins
// locks out the whole deployment.
func TestLoadServerBindsTrustedProxies(t *testing.T) {
	t.Setenv("LEOFLOW_SERVER_TRUSTED_PROXIES", "10.0.0.0/8,192.168.1.1")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	want := []string{"10.0.0.0/8", "192.168.1.1"}
	if len(c.Server.TrustedProxies) != len(want) {
		t.Fatalf("TrustedProxies = %#v, want %#v", c.Server.TrustedProxies, want)
	}
	for i := range want {
		if c.Server.TrustedProxies[i] != want[i] {
			t.Fatalf("TrustedProxies = %#v, want %#v", c.Server.TrustedProxies, want)
		}
	}
}

// TestLoadServerBindsExecutorDefaultResources proves the L0 resource defaults
// bind from env. Consequence when unbound (#725): d.Resources stays nil, so
// tasks that rely on the cluster default get no requests/limits and land in
// BestEffort QoS (first evicted under node pressure).
func TestLoadServerBindsExecutorDefaultResources(t *testing.T) {
	t.Setenv("LEOFLOW_EXECUTOR_DEFAULTS_RESOURCES_CPU", "250m")
	t.Setenv("LEOFLOW_EXECUTOR_DEFAULTS_RESOURCES_MEMORY", "256Mi")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Executor.Defaults.ResourcesCPU != "250m" {
		t.Errorf("ResourcesCPU = %q, want %q", c.Executor.Defaults.ResourcesCPU, "250m")
	}
	if c.Executor.Defaults.ResourcesMemory != "256Mi" {
		t.Errorf("ResourcesMemory = %q, want %q", c.Executor.Defaults.ResourcesMemory, "256Mi")
	}
}

// TestLoadServerBindsExecutorDefaultStaging proves the L0 staging-volume defaults
// bind from env. Consequence when unbound (#743, same class as #725): a Helm
// install has no reachable route to set them — the chart mounts no server config
// file, so the documented "config file / Helm values" path does not exist and the
// env var was never registered — leaving the per-run staging PVC to fall back to
// the cluster's default StorageClass and an unset size.
func TestLoadServerBindsExecutorDefaultStaging(t *testing.T) {
	t.Setenv("LEOFLOW_EXECUTOR_DEFAULTS_STAGING_SIZE", "10Gi")
	t.Setenv("LEOFLOW_EXECUTOR_DEFAULTS_STAGING_STORAGE_CLASS", "efs-rwx")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Executor.Defaults.StagingSize != "10Gi" {
		t.Errorf("StagingSize = %q, want %q", c.Executor.Defaults.StagingSize, "10Gi")
	}
	if c.Executor.Defaults.StagingStorageClass != "efs-rwx" {
		t.Errorf("StagingStorageClass = %q, want %q", c.Executor.Defaults.StagingStorageClass, "efs-rwx")
	}
}

// TestLoadServerBindsRedisCAFile guards the same class for managed-Redis TLS:
// the chart sets LEOFLOW_REDIS_CA_FILE when redis.caConfigMap is configured, so
// it must bind or verified TLS to Memorystore/ElastiCache silently falls back to
// system roots.
func TestLoadServerBindsRedisCAFile(t *testing.T) {
	t.Setenv("LEOFLOW_REDIS_CA_FILE", "/etc/leoflow/redis-ca/ca.crt")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Redis.CAFile != "/etc/leoflow/redis-ca/ca.crt" {
		t.Errorf("Redis.CAFile = %q, want %q", c.Redis.CAFile, "/etc/leoflow/redis-ca/ca.crt")
	}
}

// TestRegisteredKeysAreDocumented is the reverse of TestDocumentedEnvVarsBind:
// every key registered in serverDefaults must be named in the configuration
// reference, either as its LEOFLOW_* env var or — for a config-file/Helm-values
// key — in dotted form. The forward guard only runs doc → binding, so a setting
// that binds but is written down nowhere is invisible to it. That is exactly how
// auth.jwt.max_lifetime_seconds shipped as the ceiling bounding transparently
// renewed sessions — the control that makes the feature acceptable — with no
// reference row and no chart value, reachable only through extraEnv (#801, same
// class as #725 / #733 / #743).
//
// Registering a key is a promise that an operator can set it, so the reference
// row is part of shipping it. There is deliberately no exception list: an
// operator-only key still gets a row, marked as such.
func TestRegisteredKeysAreDocumented(t *testing.T) {
	// Match on WHOLE names, not substrings. A substring test lets a key that is
	// a prefix of another documented key pass for free — secrets.backend is a
	// prefix of secrets.backend_kwargs, so deleting the former's row used to
	// leave this green, shadowed by the latter. That is the one way a guard
	// like this fails silently rather than loudly.
	cols := docSettingFirstColumns(t)
	documented := make(map[string]struct{}, len(cols)*2)
	tokens := regexp.MustCompile(`[A-Za-z0-9_.]+`)
	for _, col := range cols {
		for _, tok := range tokens.FindAllString(col, -1) {
			documented[tok] = struct{}{}
		}
	}
	for key := range serverDefaults {
		suffix := strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
		env := "DEXAFLOW_" + suffix
		if _, ok := documented[env]; ok {
			continue
		}
		if _, ok := documented["LEOFLOW_"+suffix]; ok {
			continue
		}
		if _, ok := documented[key]; ok {
			continue
		}
		t.Errorf("%s (%s) is registered in serverDefaults but is named in no "+
			"settings table in website/content/reference/configuration.md, so "+
			"an operator has no documented way to reach it", key, env)
	}
}

// The loaders read DEXAFLOW_* themselves, not only when a binary mirrored the
// environment first: any caller of LoadServer or Load gets the documented names.
func TestLoadServerReadsTheNewPrefix(t *testing.T) {
	t.Setenv("DEXAFLOW_SERVER_TRUSTED_PROXIES", "10.1.0.0/16")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if len(c.Server.TrustedProxies) != 1 || c.Server.TrustedProxies[0] != "10.1.0.0/16" {
		t.Fatalf("TrustedProxies = %#v, want [10.1.0.0/16]", c.Server.TrustedProxies)
	}
}

// When both spellings are set, the current one wins, through the loader too.
func TestLoadServerNewPrefixWinsOverLegacy(t *testing.T) {
	t.Setenv("LEOFLOW_SERVER_TRUSTED_PROXIES", "192.168.0.0/16")
	t.Setenv("DEXAFLOW_SERVER_TRUSTED_PROXIES", "10.2.0.0/16")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if len(c.Server.TrustedProxies) != 1 || c.Server.TrustedProxies[0] != "10.2.0.0/16" {
		t.Fatalf("TrustedProxies = %#v, want the DEXAFLOW_ value", c.Server.TrustedProxies)
	}
}

// The CLI loader (Load) honors the new prefix too.
func TestLoadReadsTheNewPrefix(t *testing.T) {
	t.Setenv("DEXAFLOW_TOKEN", "tok-new")
	c, err := Load("", nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.Token != "tok-new" {
		t.Fatalf("Token = %q, want tok-new", c.Token)
	}
}

// TestLoadServerPoolsReadOnly proves server.pools_read_only defaults to false,
// so an upgrade changes nothing, and binds from the environment, the only
// override path a Helm install has. Consequence when unbound: an operator who
// turned the lock on to protect the slot budgets of a shared engine would see it
// silently ignored, and every tenant's admin could still resize its pools.
func TestLoadServerPoolsReadOnly(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Server.PoolsReadOnly {
		t.Fatal("server.pools_read_only must default to false")
	}

	t.Setenv("DEXAFLOW_SERVER_POOLS_READ_ONLY", "true")
	c, err = LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if !c.Server.PoolsReadOnly {
		t.Fatal("DEXAFLOW_SERVER_POOLS_READ_ONLY=true did not bind server.pools_read_only")
	}
}
