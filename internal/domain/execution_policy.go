package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ExecutionPolicyFields are the task execution fields the operator allowlist
// (executor.defaults.execution_allowlist.fields) governs, by their dag.json
// names. Each one changes where or how a task pod is placed, or how other
// cluster objects see it, so on a shared cluster it can affect other teams:
// a priority class jumps the preemption queue, a node selector, affinity or
// toleration reaches another team's dedicated nodes, a label can match another
// team's NetworkPolicy. service_account and image_pull_policy are not on the
// list.
var ExecutionPolicyFields = []string{
	"node_selector",
	"tolerations",
	"affinity",
	"topology_spread_constraints",
	"priority_class_name",
	"runtime_class_name",
	"resource_claims",
	"termination_grace_period_seconds",
	"labels",
	"annotations",
}

// Keywords of executor.defaults.execution_allowlist.fields.
const (
	// ExecutionPolicyAllFields allows every governed field (the default).
	ExecutionPolicyAllFields = "*"
	// ExecutionPolicyNoFields allows none of them. It exists because an empty
	// environment variable cannot express an empty list.
	ExecutionPolicyNoFields = "none"
)

// ExecutionPolicyConfig is executor.defaults.execution_allowlist as configured.
type ExecutionPolicyConfig struct {
	// Fields lists the governed fields a DAG author may set. Unset (nil) or "*"
	// allows every one; "none" or an explicitly empty list allows none.
	Fields []string
	// PriorityClasses, when not empty, lists the only values a task's
	// priority_class_name may take.
	PriorityClasses []string
	// ServiceAccounts, when not empty, lists the only execution.service_account
	// values a task may name (issue #537 C2). A task that names none runs as the
	// operator's default task ServiceAccount and is not checked.
	ServiceAccounts []string
	// Images, when not empty, lists the DAG images that may run (issue #537
	// C2): an exact image reference, or a prefix ending in "*" (for example
	// "registry.example.com/team-a/*"). A lone "*" allows any image.
	Images []string
}

// ExecutionPolicy is the operator's allowlist of task execution fields, service
// accounts and images (issues #629 and #537 C2). A nil *ExecutionPolicy allows
// everything, so callers need not branch on it.
type ExecutionPolicy struct {
	// fields is the set of allowed fields; nil allows every governed field.
	fields map[string]bool
	// priorityClasses is the set of allowed priority_class_name values; nil
	// allows any.
	priorityClasses []string
	// serviceAccounts is the list of allowed service_account values; nil allows
	// any.
	serviceAccounts []string
	// images is the list of allowed image references and "*"-terminated
	// prefixes; nil allows any.
	images []string
}

// ParseExecutionPolicy validates the configured allowlist. It returns nil (allow
// everything) when nothing is restricted, which is the default.
func ParseExecutionPolicy(c ExecutionPolicyConfig) (*ExecutionPolicy, error) {
	fields, err := parseAllowedFields(c.Fields)
	if err != nil {
		return nil, err
	}
	classes, err := allowedValues("priority_classes", c.PriorityClasses)
	if err != nil {
		return nil, err
	}
	sas, err := allowedValues("service_accounts", c.ServiceAccounts)
	if err != nil {
		return nil, err
	}
	images, err := parseAllowedImages(c.Images)
	if err != nil {
		return nil, err
	}
	if fields == nil && classes == nil && sas == nil && images == nil {
		return nil, nil //nolint:nilnil // no restriction is the default, not an error; nil is the allow-all policy
	}
	return &ExecutionPolicy{fields: fields, priorityClasses: classes, serviceAccounts: sas, images: images}, nil
}

// parseAllowedImages returns the allowed image references and prefixes, nil
// for "any image" (an empty list or a lone "*").
func parseAllowedImages(configured []string) ([]string, error) {
	images, err := allowedValues("images", configured)
	if err != nil {
		return nil, err
	}
	for _, img := range images {
		if strings.Contains(strings.TrimSuffix(img, "*"), "*") {
			return nil, fmt.Errorf("executor.defaults.execution_allowlist.images: %q may use \"*\" only as its last character", img)
		}
	}
	if slices.Contains(images, "*") {
		return nil, nil
	}
	return images, nil
}

// DenyAllExecutionPolicy allows no governed field. It is the safe reading of a
// configuration that failed to parse.
func DenyAllExecutionPolicy() *ExecutionPolicy {
	return &ExecutionPolicy{fields: map[string]bool{}}
}

// parseAllowedFields returns the allowed field set, nil for "allow all". Only
// an unset list (nil) allows all; an explicitly empty one allows none, as
// "none" does and as an empty list does in Helm, so writing `fields: []` to
// lock placement down can never open it.
func parseAllowedFields(configured []string) (map[string]bool, error) {
	if configured == nil {
		return nil, nil //nolint:nilnil // nil is "every field allowed"
	}
	fields := trimAll(configured)
	if len(fields) == 0 {
		return map[string]bool{}, nil
	}
	if slices.Contains(fields, ExecutionPolicyAllFields) || slices.Contains(fields, ExecutionPolicyNoFields) {
		if len(fields) > 1 {
			return nil, fmt.Errorf("executor.defaults.execution_allowlist.fields: %q and %q must be the only entry (got %v)",
				ExecutionPolicyAllFields, ExecutionPolicyNoFields, configured)
		}
		if fields[0] == ExecutionPolicyAllFields {
			return nil, nil //nolint:nilnil // nil is "every field allowed"
		}
		return map[string]bool{}, nil
	}
	allowed := map[string]bool{}
	for _, f := range fields {
		if !slices.Contains(ExecutionPolicyFields, f) {
			return nil, fmt.Errorf("executor.defaults.execution_allowlist.fields: unknown field %q (allowed: %s, or %q, or %q)",
				f, strings.Join(ExecutionPolicyFields, ", "), ExecutionPolicyAllFields, ExecutionPolicyNoFields)
		}
		allowed[f] = true
	}
	return allowed, nil
}

// allowedValues trims a configured value list and refuses a blank entry. An
// empty list returns nil ("any value").
func allowedValues(key string, configured []string) ([]string, error) {
	values := trimAll(configured)
	for _, v := range values {
		if v == "" {
			return nil, fmt.Errorf("executor.defaults.execution_allowlist.%s: empty entry", key)
		}
	}
	if len(values) == 0 {
		return nil, nil
	}
	return values, nil
}

func trimAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.TrimSpace(s))
	}
	return out
}

// setFields returns the governed fields e sets, in ExecutionPolicyFields order.
func setFields(e *Execution) []string {
	if e == nil {
		return nil
	}
	set := map[string]bool{
		"node_selector":                    len(e.NodeSelector) > 0,
		"tolerations":                      len(e.Tolerations) > 0,
		"affinity":                         len(e.Affinity) > 0,
		"topology_spread_constraints":      len(e.TopologySpreadConstraints) > 0,
		"priority_class_name":              e.PriorityClassName != "",
		"runtime_class_name":               e.RuntimeClassName != nil,
		"resource_claims":                  len(e.ResourceClaims) > 0,
		"termination_grace_period_seconds": e.TerminationGracePeriodSeconds != nil,
		"labels":                           len(e.Labels) > 0,
		"annotations":                      len(e.Annotations) > 0,
	}
	var out []string
	for _, f := range ExecutionPolicyFields {
		if set[f] {
			out = append(out, f)
		}
	}
	return out
}

// CheckTask returns why the task may not run under the policy, or nil.
func (p *ExecutionPolicy) CheckTask(t TaskSpec) error {
	if p == nil || t.Execution == nil {
		return nil
	}
	var errs []error
	if p.fields != nil {
		for _, f := range setFields(t.Execution) {
			if !p.fields[f] {
				errs = append(errs, fmt.Errorf("task %q sets execution.%s, which this cluster does not allow DAGs to set (executor.defaults.execution_allowlist.fields allows: %s)",
					t.TaskID, f, p.allowedFieldsText()))
			}
		}
	}
	if pc := t.Execution.PriorityClassName; pc != "" && p.priorityClasses != nil && !slices.Contains(p.priorityClasses, pc) {
		errs = append(errs, fmt.Errorf("task %q sets execution.priority_class_name %q, which is not in executor.defaults.execution_allowlist.priority_classes (%s)",
			t.TaskID, pc, strings.Join(p.priorityClasses, ", ")))
	}
	if sa := t.Execution.ServiceAccount; sa != "" && p.serviceAccounts != nil && !slices.Contains(p.serviceAccounts, sa) {
		errs = append(errs, fmt.Errorf("task %q sets execution.service_account %q, which is not in executor.defaults.execution_allowlist.service_accounts (%s)",
			t.TaskID, sa, strings.Join(p.serviceAccounts, ", ")))
	}
	return errors.Join(errs...)
}

// CheckServiceAccount returns why sa may not be used under the policy, or nil.
// It is the service account rule of CheckTask on its own, for a value that
// did not come from the DAG (an admission hook's change, ADR 0100 §4).
func (p *ExecutionPolicy) CheckServiceAccount(sa string) error {
	if p == nil || sa == "" || p.serviceAccounts == nil || slices.Contains(p.serviceAccounts, sa) {
		return nil
	}
	return fmt.Errorf("service account %q is not in executor.defaults.execution_allowlist.service_accounts (%s)",
		sa, strings.Join(p.serviceAccounts, ", "))
}

// CheckPriorityClass returns why pc may not be used under the policy, or nil.
// Like CheckServiceAccount, it is for a value that did not come from the DAG.
func (p *ExecutionPolicy) CheckPriorityClass(pc string) error {
	if p == nil || pc == "" || p.priorityClasses == nil || slices.Contains(p.priorityClasses, pc) {
		return nil
	}
	return fmt.Errorf("priority class %q is not in executor.defaults.execution_allowlist.priority_classes (%s)",
		pc, strings.Join(p.priorityClasses, ", "))
}

// CheckSpec returns every refusal across the DAG's tasks, or nil.
func (p *ExecutionPolicy) CheckSpec(spec *DAGSpec) error {
	if p == nil || spec == nil {
		return nil
	}
	var errs []error
	if err := p.CheckImage(spec.Image); err != nil {
		errs = append(errs, err)
	}
	for _, t := range spec.Tasks {
		if err := p.CheckTask(t); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// CheckImage returns why the DAG image may not run under the policy, or nil.
// An empty image is not checked: it is refused elsewhere.
func (p *ExecutionPolicy) CheckImage(image string) error {
	if p == nil || p.images == nil || image == "" {
		return nil
	}
	for _, allowed := range p.images {
		if prefix, ok := strings.CutSuffix(allowed, "*"); ok {
			if strings.HasPrefix(image, prefix) {
				return nil
			}
		} else if image == allowed {
			return nil
		}
	}
	return fmt.Errorf("image %q is not in executor.defaults.execution_allowlist.images (%s)",
		image, strings.Join(p.images, ", "))
}

func (p *ExecutionPolicy) allowedFieldsText() string {
	var allowed []string
	for _, f := range ExecutionPolicyFields {
		if p.fields[f] {
			allowed = append(allowed, f)
		}
	}
	if len(allowed) == 0 {
		return "none"
	}
	return strings.Join(allowed, ", ")
}
