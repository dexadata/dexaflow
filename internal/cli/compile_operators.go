package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/dexadata/dexaflow/internal/connectors"
	"github.com/dexadata/dexaflow/internal/domain"
)

// validateOperatorProvidersFile reads a produced dag.json and validates that every
// airflow_operator task's provider is declared (ADR 0040 A5).
func validateOperatorProvidersFile(path string, cfg *domain.LeoflowConfig) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: output path is operator-supplied on the CLI.
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(data, &spec); uerr != nil {
		return fmt.Errorf("parsing %s: %w", path, uerr)
	}
	return validateOperatorProviders(&spec, cfg)
}

const (
	providerPkgPrefix  = "apache-airflow-providers-"
	providerModuleRoot = "airflow.providers."
)

// bundledProviderModulePrefixes are the providers baked into the published task
// base image (runtime/Dockerfile installs apache-airflow + the Task SDK, which
// pull these). An operator from one of them needs no connectors:/dependencies:
// declaration, so they count as always-declared.
var bundledProviderModulePrefixes = []string{
	"airflow.providers.standard",
	"airflow.providers.common.sql",
	"airflow.providers.common.io",
	"airflow.providers.common.compat",
	"airflow.providers.smtp",
}

// validateOperatorProviders rejects an airflow_operator task whose provider
// package is declared in neither connectors: nor dependencies: (and is not bundled
// in the base image). This moves the failure from a runtime ModuleNotFoundError in
// the task pod to compile time, with the exact leoflow.yaml line to add — ADR 0040
// A5, the import-scan promised by ADR 0038 #2 now that operators are top-level.
func validateOperatorProviders(spec *domain.DAGSpec, cfg *domain.LeoflowConfig) error {
	deps, err := cfg.EffectiveDependencies()
	if err != nil {
		// An unknown connector is already reported by EffectiveDependencies with a
		// better message; surface it rather than masking it.
		return err
	}
	declared := append(declaredProviderModulePrefixes(deps), bundledProviderModulePrefixes...)
	var msgs []string
	for _, t := range spec.Tasks {
		if t.Type != domain.TaskTypeAirflowOperator || t.OperatorClass == "" {
			continue
		}
		if operatorProviderDeclared(t.OperatorClass, declared) {
			continue
		}
		msgs = append(msgs, operatorProviderHint(t.TaskID, t.OperatorClass))
	}
	if len(msgs) > 0 {
		return errors.New(strings.Join(msgs, "\n"))
	}
	return nil
}

// declaredProviderModulePrefixes converts each declared apache-airflow-providers-X
// dependency into its module prefix (airflow.providers.X with dashes → dots), so
// an operator class can be prefix-matched against it. A version pin or extras
// suffix is stripped first.
func declaredProviderModulePrefixes(deps []string) []string {
	var out []string
	for _, d := range deps {
		name := d
		for _, sep := range []string{"==", ">=", "<=", "~=", "!=", ">", "<", "[", " ", ";"} {
			if i := strings.Index(name, sep); i >= 0 {
				name = name[:i]
			}
		}
		name = strings.TrimSpace(name)
		if !strings.HasPrefix(name, providerPkgPrefix) {
			continue
		}
		suffix := strings.TrimPrefix(name, providerPkgPrefix)
		out = append(out, providerModuleRoot+strings.ReplaceAll(suffix, "-", "."))
	}
	return out
}

// operatorProviderDeclared reports whether the operator class's provider module is
// covered by any declared/bundled provider prefix (prefix match handles a
// provider's sub-namespaces, e.g. amazon.aws under apache-airflow-providers-amazon).
func operatorProviderDeclared(class string, declaredPrefixes []string) bool {
	for _, p := range declaredPrefixes {
		if strings.HasPrefix(class, p+".") {
			return true
		}
	}
	return false
}

// operatorProviderHint builds the actionable compile error for an undeclared
// operator provider, preferring a curated connectors: short name when the catalog
// recognizes the provider, else a best-effort dependencies: package derived from
// the import path.
func operatorProviderHint(taskID, class string) string {
	for _, c := range connectors.Catalog() {
		if c.PipPackage == "" {
			continue
		}
		prefix := providerModuleRoot + strings.ReplaceAll(strings.TrimPrefix(c.PipPackage, providerPkgPrefix), "-", ".")
		if strings.HasPrefix(class, prefix+".") {
			return fmt.Sprintf(
				"task %q uses operator %q whose provider is not installed; add it to leoflow.yaml:\n"+
					"    connectors: [%s]   # or dependencies: [%s]",
				taskID, class, c.ConnectionType, c.PipPackage)
		}
	}
	return fmt.Sprintf(
		"task %q uses operator %q whose provider is not installed; add it to leoflow.yaml:\n"+
			"    dependencies: [%s]",
		taskID, class, bestEffortProviderPackage(class))
}

// bestEffortProviderPackage derives apache-airflow-providers-<name> from an
// operator class for a provider not in the curated catalog: the module segments
// between airflow.providers. and the operators/sensors/transfers/hooks boundary,
// dash-joined.
func bestEffortProviderPackage(class string) string {
	rest := strings.TrimPrefix(class, providerModuleRoot)
	var provider []string
	for _, s := range strings.Split(rest, ".") {
		if s == "operators" || s == "sensors" || s == "transfers" || s == "hooks" {
			break
		}
		provider = append(provider, s)
	}
	return providerPkgPrefix + strings.Join(provider, "-")
}
