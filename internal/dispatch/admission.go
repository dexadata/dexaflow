package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/pkg/admission"
)

// Admission defaults (ADR 0100 §5).
const (
	// DefaultAdmissionTimeout bounds one hook call. Dispatch is synchronous by
	// default and runs on the scheduler tick, so it is short.
	DefaultAdmissionTimeout = 500 * time.Millisecond
	// admissionBreakerThreshold is the number of consecutive failed calls of
	// one hook after which that hook is skipped for admissionBreakerOpen,
	// instead of every attempt waiting out the timeout. After the window one
	// call probes the hook; a failed probe opens the breaker again.
	admissionBreakerThreshold = 5
	admissionBreakerOpen      = 30 * time.Second
	// maxAdmissionText caps a hook's refusal or hold reason, and the text of
	// a hook's error, as the task instance shows them.
	maxAdmissionText = 512
)

// Admission outcomes, the label of the admission metric. They are disjoint:
// breaker_open is an attempt held because a hook was skipped, fail_open one
// admitted unchecked because a hook failed or was skipped.
const (
	AdmissionAdmitted    = "admitted"
	AdmissionChanged     = "changed"
	AdmissionHeld        = "held"
	AdmissionRefused     = "refused"
	AdmissionError       = "error"
	AdmissionFailOpen    = "fail_open"
	AdmissionBreakerOpen = "breaker_open"
)

// AdmissionRecorder counts each admission outcome and how long the hooks
// took. observability.Metrics satisfies it.
type AdmissionRecorder interface {
	RecordAdmission(outcome string, took time.Duration)
}

// AdmissionOptions configure the admission hooks of a Dispatcher.
type AdmissionOptions struct {
	// Timeout bounds each hook call; zero uses DefaultAdmissionTimeout.
	Timeout time.Duration
	// FailOpen admits an attempt unchanged when a hook errors, times out,
	// panics or is skipped by its breaker, instead of holding it. A hook's
	// Hold is never failed open.
	FailOpen bool
	// Recorder counts outcomes; nil does not.
	Recorder AdmissionRecorder
}

// admissionChain is the dispatcher's ordered list of hooks.
type admissionChain struct {
	hooks    []*admissionHook
	timeout  time.Duration
	failOpen bool
	recorder AdmissionRecorder
	now      func() time.Time
}

// admissionHook is one hook and its breaker. Each hook has its own, so one
// failing hook never skips the others.
type admissionHook struct {
	hook admission.Hook

	mu        sync.Mutex
	failures  int
	openUntil time.Time
	probing   bool
}

// SetAdmission installs the admission hooks, called in order for every
// attempt after the built-in refusals and before the agent token is minted
// (ADR 0100). No hooks leave dispatch exactly as it was.
func (d *Dispatcher) SetAdmission(hooks []admission.Hook, opts AdmissionOptions) {
	if len(hooks) == 0 {
		d.admission = nil
		return
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultAdmissionTimeout
	}
	if opts.FailOpen {
		slog.Warn("admission hooks fail open: an attempt runs unchanged when a hook errors, times out or panics",
			"hooks", len(hooks))
	}
	c := &admissionChain{timeout: timeout, failOpen: opts.FailOpen, recorder: opts.Recorder, now: time.Now}
	for _, h := range hooks {
		c.hooks = append(c.hooks, &admissionHook{hook: h})
	}
	d.admission = c
}

// admissionUnavailable is the cause of an attempt the hooks hold. The
// scheduler names it in the task's failure once the hold outlasts the ADR
// 0093 window, instead of blaming the Kubernetes API.
type admissionUnavailable struct {
	subject string
	err     error
}

func (e *admissionUnavailable) Error() string { return e.err.Error() }
func (e *admissionUnavailable) Unwrap() error { return e.err }

// UnavailableSubject says what kept the attempt waiting.
func (e *admissionUnavailable) UnavailableSubject() string { return e.subject }

func unavailable(subject string, err error) error {
	return &admissionUnavailable{subject: subject, err: err}
}

// admit runs the admission hooks on an attempt that passed the built-in
// refusals. It returns the task to dispatch (with the hooks' changes) and,
// when the hooks changed it, the task's own execution settings for the
// executor's allowlist re-check. A non-nil error comes with the disposition
// to return: Refused for a refusal, Unavailable for a hold or a hook that
// failed.
func (d *Dispatcher) admit(ctx context.Context, runID, dagID string, task domain.TaskSpec, r Resolved, sourceMode bool) (domain.TaskSpec, *domain.Execution, executor.Disposition, error) {
	c := d.admission
	if c == nil {
		return task, nil, executor.Dispatched, nil
	}
	start := c.now()
	req := d.admissionRequest(runID, dagID, task, r, sourceMode)
	var total admission.Changes
	for i, h := range c.hooks {
		n := i + 1
		if err := ctx.Err(); err != nil {
			return task, nil, executor.Unavailable, unavailable("admission hook unavailable", fmt.Errorf("admission hook unavailable: %w", err))
		}
		if !h.allow(c.now()) {
			return d.admissionFailed(c, task, runID, dagID, AdmissionBreakerOpen, start,
				fmt.Errorf("admission hook unavailable: hook %d skipped after repeated failures", n))
		}
		dec, err := c.call(ctx, h.hook, req)
		if err != nil {
			if ctx.Err() != nil {
				// Shutdown or a canceled tick, not the hook's fault: hold the
				// attempt without failing open or counting it against the hook.
				h.release()
				return task, nil, executor.Unavailable, unavailable("admission hook unavailable", fmt.Errorf("admission hook unavailable: %w", ctx.Err()))
			}
			h.fail(c.now())
			return d.admissionFailed(c, task, runID, dagID, AdmissionError, start,
				fmt.Errorf("admission hook unavailable: hook %d: %s", n, cleanText(err.Error(), "error")))
		}
		h.succeed()
		if reason, held := dec.Held(); held {
			c.record(AdmissionHeld, start)
			return task, nil, executor.Unavailable, unavailable("held by an admission hook",
				fmt.Errorf("admission: held: %s", cleanText(reason, "held by an admission hook")))
		}
		if reason, refused := dec.Refused(); refused {
			c.record(AdmissionRefused, start)
			return task, nil, executor.Refused, fmt.Errorf("admission: %s", cleanText(reason, "refused by an admission hook"))
		}
		ch := dec.Changes()
		if ch.IsZero() {
			continue
		}
		if err := d.checkChanges(n, ch); err != nil {
			c.record(AdmissionRefused, start)
			return task, nil, executor.Refused, err
		}
		applyToRequest(req, ch)
		total = mergeChanges(total, ch)
	}
	if total.IsZero() {
		c.record(AdmissionAdmitted, start)
		return task, nil, executor.Dispatched, nil
	}
	c.record(AdmissionChanged, start)
	var authored domain.Execution
	if task.Execution != nil {
		authored = *task.Execution
	}
	task.Execution = applyChanges(task.Execution, total)
	return task, &authored, executor.Dispatched, nil
}

// admissionFailed holds the attempt (Unavailable, so it is offered again
// without spending dispatch attempts until the ADR 0093 window passes), or
// admits it unchanged when the hooks fail open.
func (d *Dispatcher) admissionFailed(c *admissionChain, task domain.TaskSpec, runID, dagID, outcome string, start time.Time, cause error) (domain.TaskSpec, *domain.Execution, executor.Disposition, error) {
	if c.failOpen {
		c.record(AdmissionFailOpen, start)
		slog.Warn("admission hook failed; admitting the attempt unchanged (fail open)",
			"run", runID, "dag", dagID, "task", task.TaskID, "outcome", outcome, "error", cause)
		return task, nil, executor.Dispatched, nil
	}
	c.record(outcome, start)
	return task, nil, executor.Unavailable, unavailable("admission hook unavailable", cause)
}

// call runs one hook with the chain's timeout. A hook that ignores its
// context is not waited for past the timeout; its goroutine ends when the
// hook returns, and the hook's breaker bounds how often that can happen. A
// panic is recovered, logged with its stack and reported as an error.
func (c *admissionChain) call(ctx context.Context, h admission.Hook, req *admission.Request) (admission.Decision, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	type result struct {
		dec admission.Decision
		err error
	}
	done := make(chan result, 1)
	in := cloneRequest(req)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("admission hook panicked", "panic", p, "stack", string(debug.Stack()))
				done <- result{err: fmt.Errorf("panic: %v", p)}
			}
		}()
		dec, err := h.Admit(ctx, in)
		done <- result{dec: dec, err: err}
	}()
	select {
	case res := <-done:
		return res.dec, res.err
	case <-ctx.Done():
		return admission.Decision{}, fmt.Errorf("no answer within %s: %w", c.timeout, ctx.Err())
	}
}

// allow reports whether the hook may be called now. While the breaker is
// open it may not; once the open window passed, one caller probes the hook
// and the others still skip it until the probe ends.
func (h *admissionHook) allow(now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.openUntil.IsZero() {
		return true
	}
	if now.Before(h.openUntil) || h.probing {
		return false
	}
	h.probing = true
	return true
}

func (h *admissionHook) fail(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failures++
	if h.probing || h.failures >= admissionBreakerThreshold {
		h.openUntil = now.Add(admissionBreakerOpen)
		h.failures = 0
		h.probing = false
	}
}

func (h *admissionHook) succeed() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failures = 0
	h.openUntil = time.Time{}
	h.probing = false
}

// release ends a probe that told nothing about the hook (the caller's
// context ended), so the next caller probes again.
func (h *admissionHook) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.probing = false
}

func (c *admissionChain) record(outcome string, start time.Time) {
	if c.recorder != nil {
		c.recorder.RecordAdmission(outcome, c.now().Sub(start))
	}
}

// admissionRequest builds the hook's view of the attempt, with the effective
// resources and service account (ADR 0100 §1).
func (d *Dispatcher) admissionRequest(runID, dagID string, task domain.TaskSpec, r Resolved, sourceMode bool) *admission.Request {
	req := &admission.Request{
		TenantID:   r.TenantID,
		DagID:      dagID,
		RunID:      runID,
		TaskID:     task.TaskID,
		TryNumber:  r.TryNumber,
		Operator:   string(task.Type),
		Image:      r.Image,
		SourceMode: sourceMode,
		Resources:  toAdmissionResources(d.taskResources(task)),
	}
	if e := task.Execution; e != nil {
		req.Execution = admission.Execution{
			ServiceAccount:    e.ServiceAccount,
			NodeSelector:      maps.Clone(e.NodeSelector),
			Tolerations:       toAdmissionTolerations(e.Tolerations),
			PriorityClassName: e.PriorityClassName,
			Labels:            maps.Clone(e.Labels),
			Annotations:       maps.Clone(e.Annotations),
		}
	}
	req.ServiceAccountChosen = req.Execution.ServiceAccount != ""
	if !req.ServiceAccountChosen {
		req.Execution.ServiceAccount = d.defaultTaskServiceAccount
	}
	return req
}

// checkChanges refuses a change a hook may not make: a reserved label or
// annotation key, a key or value Kubernetes would reject, a malformed
// toleration, or a priority class or service account the allowlist does not
// list. The operator's default task ServiceAccount is always allowed, as in
// the executor's re-check. The refusal names the hook, so its owner can fix
// it.
func (d *Dispatcher) checkChanges(hook int, ch admission.Changes) error {
	errs := checkMetadata(ch)
	for _, t := range ch.Tolerations {
		if err := checkToleration(t); err != nil {
			errs = append(errs, err)
		}
	}
	if pc := ch.PriorityClassName; pc != "" {
		if msgs := validation.IsDNS1123Subdomain(pc); len(msgs) > 0 {
			errs = append(errs, fmt.Errorf("priority class %q: %s", pc, strings.Join(msgs, "; ")))
		} else if err := d.defaults.ExecutionPolicy.CheckPriorityClass(pc); err != nil {
			errs = append(errs, err)
		}
	}
	if sa := ch.ServiceAccount; sa != "" && sa != d.defaultTaskServiceAccount {
		if msgs := validation.IsDNS1123Subdomain(sa); len(msgs) > 0 {
			errs = append(errs, fmt.Errorf("service account %q: %s", sa, strings.Join(msgs, "; ")))
		} else if err := d.defaults.ExecutionPolicy.CheckServiceAccount(sa); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("admission: hook %d: %w", hook, errors.Join(errs...))
}

// checkMetadata checks the node selector, label and annotation keys a hook
// set: none reserved, and each key and value one Kubernetes accepts.
func checkMetadata(ch admission.Changes) []error {
	var errs []error
	for _, m := range []struct {
		what   string
		kv     map[string]string
		values bool
	}{{"node selector", ch.NodeSelector, true}, {"label", ch.Labels, true}, {"annotation", ch.Annotations, false}} {
		for k, v := range m.kv {
			if admission.IsReservedKey(k) {
				errs = append(errs, fmt.Errorf("%s key %q is reserved", m.what, k))
				continue
			}
			if msgs := validation.IsQualifiedName(k); len(msgs) > 0 {
				errs = append(errs, fmt.Errorf("%s key %q: %s", m.what, k, strings.Join(msgs, "; ")))
			}
			if m.values {
				if msgs := validation.IsValidLabelValue(v); len(msgs) > 0 {
					errs = append(errs, fmt.Errorf("%s %q value %q: %s", m.what, k, v, strings.Join(msgs, "; ")))
				}
			}
		}
	}
	return errs
}

func checkToleration(t admission.Toleration) error {
	switch t.Operator {
	case "", "Equal":
	case "Exists":
		if t.Value != "" {
			return fmt.Errorf("toleration %q: operator Exists takes no value", t.Key)
		}
	default:
		return fmt.Errorf("toleration %q: unknown operator %q", t.Key, t.Operator)
	}
	switch t.Effect {
	case "", "NoSchedule", "PreferNoSchedule":
		if t.TolerationSeconds != nil {
			return fmt.Errorf("toleration %q: toleration seconds need effect NoExecute", t.Key)
		}
	case "NoExecute":
	default:
		return fmt.Errorf("toleration %q: unknown effect %q", t.Key, t.Effect)
	}
	if t.Key != "" {
		if msgs := validation.IsQualifiedName(t.Key); len(msgs) > 0 {
			return fmt.Errorf("toleration key %q: %s", t.Key, strings.Join(msgs, "; "))
		}
	} else if t.Operator != "Exists" {
		return errors.New("toleration with no key needs operator Exists")
	}
	return nil
}

// applyToRequest shows a later hook what an earlier one changed.
func applyToRequest(req *admission.Request, ch admission.Changes) {
	e := &req.Execution
	e.NodeSelector = mergeMap(e.NodeSelector, ch.NodeSelector)
	e.Tolerations = append(e.Tolerations, ch.Tolerations...)
	if ch.PriorityClassName != "" {
		e.PriorityClassName = ch.PriorityClassName
	}
	if ch.ServiceAccount != "" {
		e.ServiceAccount = ch.ServiceAccount
	}
	e.Labels = mergeMap(e.Labels, ch.Labels)
	e.Annotations = mergeMap(e.Annotations, ch.Annotations)
}

// mergeChanges folds a later hook's changes over the earlier ones.
func mergeChanges(a, b admission.Changes) admission.Changes {
	a.NodeSelector = mergeMap(a.NodeSelector, b.NodeSelector)
	a.Tolerations = append(a.Tolerations, b.Tolerations...)
	if b.PriorityClassName != "" {
		a.PriorityClassName = b.PriorityClassName
	}
	if b.ServiceAccount != "" {
		a.ServiceAccount = b.ServiceAccount
	}
	a.Labels = mergeMap(a.Labels, b.Labels)
	a.Annotations = mergeMap(a.Annotations, b.Annotations)
	return a
}

// applyChanges returns a copy of the task's execution settings with the
// hooks' changes applied; the task's own value is never modified.
func applyChanges(own *domain.Execution, ch admission.Changes) *domain.Execution {
	var e domain.Execution
	if own != nil {
		e = *own
		e.NodeSelector = maps.Clone(own.NodeSelector)
		e.Tolerations = cloneTolerations(own.Tolerations)
		e.Labels = maps.Clone(own.Labels)
		e.Annotations = maps.Clone(own.Annotations)
	}
	e.NodeSelector = mergeMap(e.NodeSelector, ch.NodeSelector)
	for _, t := range ch.Tolerations {
		e.Tolerations = append(e.Tolerations, fromAdmissionToleration(t))
	}
	if ch.PriorityClassName != "" {
		e.PriorityClassName = ch.PriorityClassName
	}
	if ch.ServiceAccount != "" {
		e.ServiceAccount = ch.ServiceAccount
	}
	e.Labels = mergeMap(e.Labels, ch.Labels)
	e.Annotations = mergeMap(e.Annotations, ch.Annotations)
	return &e
}

func mergeMap(dst, src map[string]string) map[string]string {
	if len(src) == 0 {
		return dst
	}
	out := make(map[string]string, len(dst)+len(src))
	maps.Copy(out, dst)
	maps.Copy(out, src)
	return out
}

func cloneTolerations(in []map[string]any) []map[string]any {
	if in == nil {
		return nil
	}
	out := make([]map[string]any, len(in))
	for i, t := range in {
		out[i] = maps.Clone(t)
	}
	return out
}

// toAdmissionTolerations converts a task's tolerations, as the DAG spec
// carries them (the Kubernetes JSON field names), to the hook's typed form.
func toAdmissionTolerations(in []map[string]any) []admission.Toleration {
	if in == nil {
		return nil
	}
	out := make([]admission.Toleration, 0, len(in))
	for _, m := range in {
		str := func(k string) string {
			if s, ok := m[k].(string); ok {
				return s
			}
			return ""
		}
		t := admission.Toleration{Key: str("key"), Operator: str("operator"), Value: str("value"), Effect: str("effect")}
		if s, ok := tolerationSeconds(m["tolerationSeconds"]); ok {
			t.TolerationSeconds = &s
		}
		out = append(out, t)
	}
	return out
}

func tolerationSeconds(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if n == math.Trunc(n) && math.Abs(n) < math.MaxInt64 {
			return int64(n), true
		}
	}
	return 0, false
}

func fromAdmissionToleration(t admission.Toleration) map[string]any {
	m := map[string]any{}
	for k, v := range map[string]string{"key": t.Key, "operator": t.Operator, "value": t.Value, "effect": t.Effect} {
		if v != "" {
			m[k] = v
		}
	}
	if t.TolerationSeconds != nil {
		m["tolerationSeconds"] = *t.TolerationSeconds
	}
	return m
}

// cloneRequest gives each hook its own copy, so a hook that edits the
// request (or keeps it past its return) cannot touch what the next one sees.
func cloneRequest(r *admission.Request) *admission.Request {
	c := *r
	c.Execution.NodeSelector = maps.Clone(r.Execution.NodeSelector)
	c.Execution.Labels = maps.Clone(r.Execution.Labels)
	c.Execution.Annotations = maps.Clone(r.Execution.Annotations)
	if r.Execution.Tolerations != nil {
		c.Execution.Tolerations = make([]admission.Toleration, len(r.Execution.Tolerations))
		for i, t := range r.Execution.Tolerations {
			if t.TolerationSeconds != nil {
				s := *t.TolerationSeconds
				t.TolerationSeconds = &s
			}
			c.Execution.Tolerations[i] = t
		}
	}
	return &c
}

func toAdmissionResources(r domain.Resources) admission.Resources {
	var out admission.Resources
	if r.Requests != nil {
		out.Requests = admission.Quantity{CPU: r.Requests.CPU, Memory: r.Requests.Memory, EphemeralStorage: r.Requests.EphemeralStorage}
	}
	if r.Limits != nil {
		out.Limits = admission.Quantity{CPU: r.Limits.CPU, Memory: r.Limits.Memory, EphemeralStorage: r.Limits.EphemeralStorage}
	}
	return out
}

// cleanText makes text from a hook safe to show on a task instance: control
// and format characters (bidirectional overrides among them) become spaces,
// and it is cut to maxAdmissionText bytes at a rune boundary. Empty text
// becomes fallback.
func cleanText(text, fallback string) string {
	text = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(text, "?")))
	if text == "" {
		return fallback
	}
	if len(text) <= maxAdmissionText {
		return text
	}
	cut := maxAdmissionText
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}
