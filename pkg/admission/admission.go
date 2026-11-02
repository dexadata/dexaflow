// Package admission is the contract of the dispatch admission hook (ADR 0100):
// operator code that admits, changes or refuses each task attempt after the
// server's own checks and before the attempt's agent token is minted.
//
// A hook sees a copy of the attempt ([Request]) and answers with a
// [Decision]: [Admit] runs it as it is, [AdmitWith] runs it with the pod
// placement and metadata changed, [Hold] keeps it waiting and offers it
// again, and [Refuse] fails the task for good with a reason the task instance
// shows. An error means the hook itself is broken: the attempt is held too,
// so a hook that is briefly down never lets a task run unchecked, unless the
// operator chose to fail open.
//
// What a hook may change is limited on purpose: the node selector,
// tolerations, priority class, service account, and labels and annotations
// outside the keys Dexaflow reserves. The image, the command, the
// environment, the namespace and the resources stay as the server chose
// them, so the attempt is charged the pool slots it was admitted with.
//
// Everything exported here follows the pkg/ compatibility rule of ADR 0099:
// no breaking change in a patch release.
package admission

import (
	"context"
	"strings"
)

// Hook admits, changes, holds or refuses one task attempt. The server calls
// Admit once per attempt, with a deadline on ctx, and stops waiting when the
// deadline passes. A hook must return once ctx is done: one that ignores it
// keeps running in the background after the server moved on. Admit must not
// keep req after it returns.
type Hook interface {
	Admit(ctx context.Context, req *Request) (Decision, error)
}

// HookFunc lets an ordinary function be a Hook.
type HookFunc func(ctx context.Context, req *Request) (Decision, error)

// Admit calls f(ctx, req).
func (f HookFunc) Admit(ctx context.Context, req *Request) (Decision, error) {
	return f(ctx, req)
}

// Request describes the attempt about to run. It is a copy: changing it has
// no effect, a hook answers with a Decision instead. It never carries the
// task's environment, its source, secrets or the attempt's token.
type Request struct {
	// TenantID, DagID, RunID and TaskID name the attempt; TryNumber counts its
	// tries from 1.
	TenantID  string
	DagID     string
	RunID     string
	TaskID    string
	TryNumber int
	// Operator is the task's operator type, for example "python" or "bash".
	Operator string
	// Image is the image the run resolved, the one the pod would run.
	Image string
	// SourceMode reports whether a Cluster attempt runs the DAG source on the
	// operator's runtime image (ADR 0067). Lite always runs from source and
	// reports false.
	SourceMode bool
	// Resources are the effective requests and limits: the task's own, or the
	// resource unit's or the platform defaults' where the server applies them.
	Resources Resources
	// Execution is the task's placement and pod metadata, with the service
	// account after the server's default is applied.
	Execution Execution
	// ServiceAccountChosen reports whether the task named its service account
	// itself; false means Execution.ServiceAccount is the server's default (or
	// empty when no default is configured).
	ServiceAccountChosen bool
}

// Resources are the task pod's requests and limits.
type Resources struct {
	Requests Quantity
	Limits   Quantity
}

// Quantity holds Kubernetes quantities, for example "500m" or "1Gi". An empty
// string means not set.
type Quantity struct {
	CPU              string
	Memory           string
	EphemeralStorage string
}

// Execution is the subset of a task's execution settings a hook sees.
type Execution struct {
	ServiceAccount    string
	NodeSelector      map[string]string
	Tolerations       []Toleration
	PriorityClassName string
	Labels            map[string]string
	Annotations       map[string]string
}

// Toleration is a Kubernetes pod toleration. Empty fields are left out of
// the pod.
type Toleration struct {
	Key      string
	Operator string // "Exists" or "Equal"; empty means Equal
	Value    string
	Effect   string // "NoSchedule", "PreferNoSchedule", "NoExecute" or empty for all
	// TolerationSeconds, for NoExecute only, is how long the pod stays bound
	// to a node that gains the taint; nil means forever.
	TolerationSeconds *int64
}

// Changes are what a hook may change on the attempt. A zero field changes
// nothing: map entries are merged over the task's own (the hook's keys win),
// tolerations are appended, and a non-empty string replaces the task's value.
type Changes struct {
	NodeSelector map[string]string
	Tolerations  []Toleration
	// PriorityClassName and ServiceAccount must be allowed by the server's
	// execution allowlist (execution_allowlist.priority_classes and
	// service_accounts) when those lists are set.
	PriorityClassName string
	// ServiceAccount must be allowed by the server's execution allowlist
	// (execution_allowlist.service_accounts) when that list is set.
	ServiceAccount string
	// Labels and Annotations may not use a key under one of ReservedPrefixes.
	Labels      map[string]string
	Annotations map[string]string
}

var reservedPrefixes = []string{"leoflow.io/", "dexaflow.io/"}

// ReservedPrefixes returns the label and annotation key prefixes a hook may
// not set: the server decides what a pod is from keys under them.
func ReservedPrefixes() []string { return append([]string(nil), reservedPrefixes...) }

// IsReservedKey reports whether a label or annotation key falls under one of
// ReservedPrefixes.
func IsReservedKey(key string) bool {
	for _, p := range reservedPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// IsZero reports whether c changes nothing.
func (c Changes) IsZero() bool {
	return len(c.NodeSelector) == 0 && len(c.Tolerations) == 0 && c.PriorityClassName == "" &&
		c.ServiceAccount == "" && len(c.Labels) == 0 && len(c.Annotations) == 0
}

type verdict int

const (
	admitted verdict = iota
	held
	refused
)

// Decision is a hook's answer. Its zero value admits the attempt unchanged.
type Decision struct {
	verdict verdict
	changes Changes
	reason  string
}

// Admit runs the attempt as it is.
func Admit() Decision { return Decision{} }

// AdmitWith runs the attempt with c applied.
func AdmitWith(c Changes) Decision { return Decision{changes: c} }

// Hold keeps the attempt waiting and offers it again later, for a condition
// that passes (a freeze window, a budget that resets). It is not a failure of
// the hook: it never fails open and never counts toward skipping the hook. A
// held attempt waits within the same window as an unreachable cluster (ADR
// 0093) and fails with reason once that window is spent.
func Hold(reason string) Decision { return Decision{verdict: held, reason: reason} }

// Refuse fails the task for good, without retries, and the task instance
// shows reason. For a temporary condition use Hold.
func Refuse(reason string) Decision { return Decision{verdict: refused, reason: reason} }

// Refused reports whether d refuses the attempt, and why.
func (d Decision) Refused() (string, bool) { return d.reason, d.verdict == refused }

// Held reports whether d holds the attempt, and why.
func (d Decision) Held() (string, bool) { return d.reason, d.verdict == held }

// Changes returns what d changes on an admitted attempt.
func (d Decision) Changes() Changes { return d.changes }
