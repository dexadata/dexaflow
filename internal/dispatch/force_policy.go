package dispatch

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/pkg/admission"
)

// ForcePolicy is the operator's executor policy (ADR 0063, executor.policy):
// pod fields set on every task pod whatever the DAG says. It runs as the last
// admission step (ADR 0100 §7), after the operator's hooks, so neither a DAG
// nor a hook can undo it. What a DAG may set at all is the execution
// allowlist (executor.defaults.execution_allowlist), not this policy.
//
// A nil *ForcePolicy changes nothing, so callers need not branch on it.
type ForcePolicy struct {
	runtimeClassName string
	serviceAccount   string
	nodeSelector     map[string]string
	tolerations      []map[string]any
}

// forcePolicyDoc is the YAML shape of executor.policy. Only force exists;
// unknown keys fail the parse, so a typo or a restrict rule from the first
// draft of ADR 0063 stops the boot instead of enforcing nothing.
type forcePolicyDoc struct {
	Force *forceRules `yaml:"force"`
}

type forceRules struct {
	RuntimeClassName string            `yaml:"runtime_class_name"`
	ServiceAccount   string            `yaml:"service_account"`
	NodeSelector     map[string]string `yaml:"node_selector"`
	Tolerations      []forceToleration `yaml:"tolerations"`
}

type forceToleration struct {
	Key               string `yaml:"key"`
	Operator          string `yaml:"operator"`
	Value             string `yaml:"value"`
	Effect            string `yaml:"effect"`
	TolerationSeconds *int64 `yaml:"toleration_seconds"`
}

// ParseForcePolicy decodes and validates the executor.policy document. An
// empty document, or one that forces nothing, is no policy (nil).
func ParseForcePolicy(doc []byte) (*ForcePolicy, error) {
	if len(bytes.TrimSpace(doc)) == 0 {
		return nil, nil //nolint:nilnil // no policy is the default, not an error
	}
	var d forcePolicyDoc
	dec := yaml.NewDecoder(bytes.NewReader(doc))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("executor.policy: %w", err)
	}
	if d.Force == nil {
		return nil, nil //nolint:nilnil // a document with no rule is no policy
	}
	f := d.Force
	var errs []error
	if f.RuntimeClassName != "" {
		if msgs := validation.IsDNS1123Subdomain(f.RuntimeClassName); len(msgs) > 0 {
			errs = append(errs, fmt.Errorf("force.runtime_class_name %q: %s", f.RuntimeClassName, strings.Join(msgs, "; ")))
		}
	}
	if f.ServiceAccount != "" {
		if msgs := validation.IsDNS1123Subdomain(f.ServiceAccount); len(msgs) > 0 {
			errs = append(errs, fmt.Errorf("force.service_account %q: %s", f.ServiceAccount, strings.Join(msgs, "; ")))
		}
	}
	for _, err := range checkMetadata(admission.Changes{NodeSelector: f.NodeSelector}) {
		errs = append(errs, fmt.Errorf("force.node_selector: %w", err))
	}
	p := &ForcePolicy{
		runtimeClassName: f.RuntimeClassName,
		serviceAccount:   f.ServiceAccount,
		nodeSelector:     maps.Clone(f.NodeSelector),
	}
	for i, t := range f.Tolerations {
		at := admission.Toleration{Key: t.Key, Operator: t.Operator, Value: t.Value, Effect: t.Effect, TolerationSeconds: t.TolerationSeconds}
		if err := checkToleration(at); err != nil {
			errs = append(errs, fmt.Errorf("force.tolerations[%d]: %w", i, err))
			continue
		}
		p.tolerations = append(p.tolerations, fromAdmissionToleration(at))
	}
	if len(errs) > 0 {
		sortErrors(errs)
		return nil, fmt.Errorf("executor.policy: %w", errors.Join(errs...))
	}
	if p.runtimeClassName == "" && p.serviceAccount == "" && len(p.nodeSelector) == 0 && len(p.tolerations) == 0 {
		return nil, nil //nolint:nilnil // an empty force block is no policy
	}
	return p, nil
}

// sortErrors orders errors by text, so a refusal that lists several problems
// (map iteration order) reads the same on every boot.
func sortErrors(errs []error) {
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
}

// ServiceAccount is the service account the policy forces, "" for none.
func (p *ForcePolicy) ServiceAccount() string {
	if p == nil {
		return ""
	}
	return p.serviceAccount
}

// Summary lists the fields the policy forces, for the boot log.
func (p *ForcePolicy) Summary() []string {
	if p == nil {
		return nil
	}
	var out []string
	if p.runtimeClassName != "" {
		out = append(out, "runtime_class_name")
	}
	if p.serviceAccount != "" {
		out = append(out, "service_account")
	}
	if len(p.nodeSelector) > 0 {
		out = append(out, "node_selector")
	}
	if len(p.tolerations) > 0 {
		out = append(out, "tolerations")
	}
	return out
}

// Apply returns task with the forced fields set, and whether anything was
// forced. The runtime class and the service account replace the task's, the
// node selector is merged over the task's (the policy's keys win), and the
// tolerations are appended. Neither the task's maps nor the policy's are
// shared with the result.
func (p *ForcePolicy) Apply(task domain.TaskSpec) (domain.TaskSpec, bool) {
	if p == nil {
		return task, false
	}
	var e domain.Execution
	if own := task.Execution; own != nil {
		e = *own
		e.NodeSelector = maps.Clone(own.NodeSelector)
		e.Tolerations = cloneTolerations(own.Tolerations)
		e.Labels = maps.Clone(own.Labels)
		e.Annotations = maps.Clone(own.Annotations)
	}
	if p.runtimeClassName != "" {
		rc := p.runtimeClassName
		e.RuntimeClassName = &rc
	}
	if p.serviceAccount != "" {
		e.ServiceAccount = p.serviceAccount
	}
	e.NodeSelector = mergeMap(e.NodeSelector, p.nodeSelector)
	for _, t := range p.tolerations {
		e.Tolerations = append(e.Tolerations, maps.Clone(t))
	}
	task.Execution = &e
	return task, true
}
