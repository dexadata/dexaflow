package api

import (
	"context"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// The register dry run reports the pods the version's tasks would run in
// (#1889), so a deploy learns before the build what otherwise fails only
// when a pod starts: a task ServiceAccount that does not exist, or one with
// no pull secret, and whether the pods refuse an image that runs as root.
// Dexaflow's own field of the dry run answer, not an Airflow one.

// ServiceAccountReader reads the ServiceAccounts of the namespace task pods
// run in. It lives here, next to the dry run that consumes it; the
// executor's Kubernetes client implements it.
type ServiceAccountReader interface {
	// ImagePullSecrets returns how many imagePullSecrets the ServiceAccount
	// name carries, and found false when it does not exist. An error means
	// the lookup could not tell.
	ImagePullSecrets(ctx context.Context, name string) (count int, found bool, err error)
}

// TaskPodSettings is how this server starts task pods, as the register dry
// run reports it. The zero value is a server that starts no task pods.
type TaskPodSettings struct {
	// Enabled is true when this server dispatches tasks to pods.
	Enabled bool
	// RunAsNonRoot is executor.run_tasks_as_non_root: task pods refuse an
	// image that runs as root, or as a user that is not a number.
	RunAsNonRoot bool
	// DefaultServiceAccount is executor.task_service_account, the account
	// of a task that names none; "" runs it as the namespace's default.
	DefaultServiceAccount string
	// ServiceAccounts reads the task ServiceAccounts; nil reports each one
	// unchecked, with Unread as the reason.
	ServiceAccounts ServiceAccountReader
	// Unread says why ServiceAccounts is nil.
	Unread string
}

// The states of a task ServiceAccount in the dry run answer.
const (
	serviceAccountFound     = "found"
	serviceAccountMissing   = "missing"
	serviceAccountUnchecked = "unchecked"
)

// namespaceDefaultServiceAccount is the account Kubernetes runs a pod as
// when its spec names none.
const namespaceDefaultServiceAccount = "default"

// maxServiceAccountLookups bounds the lookups of one dry run, so a spec
// naming many accounts cannot turn a dry run into a burst of API calls.
const maxServiceAccountLookups = 8

// serviceAccountLookupTimeout bounds all the lookups of one dry run.
const serviceAccountLookupTimeout = 5 * time.Second

// The fixed details of an unchecked account: an error of the lookup is
// logged, never answered, since it can name cluster internals.
const (
	serviceAccountLookupFailed = "the control plane could not read it; its log has the cause"
)

// serviceAccountInvalidName is the detail of a missing account whose name
// is not a DNS-1123 subdomain, so no ServiceAccount can carry it.
const serviceAccountInvalidName = "not a valid ServiceAccount name: lower case letters, digits, '-' and '.', starting and ending with a letter or digit, at most 253 characters"

// dns1123Subdomain is a DNS-1123 subdomain, the form of a ServiceAccount
// name, short of its length bound.
var dns1123Subdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// validServiceAccountName reports whether name can name a ServiceAccount:
// a DNS-1123 subdomain of at most 253 characters. It is checked here, not
// with the Kubernetes validation package, so the API imports no Kubernetes
// package (ADR 0085).
func validServiceAccountName(name string) bool {
	return len(name) <= 253 && dns1123Subdomain.MatchString(name)
}

// serviceAccountTooMany is the detail of an account past
// maxServiceAccountLookups.
var serviceAccountTooMany = "not looked up: a dry run looks up at most " + strconv.Itoa(maxServiceAccountLookups) + " ServiceAccounts"

// taskPodsReport is the task_pods field of the dry run answer.
type taskPodsReport struct {
	RunAsNonRoot    bool                  `json:"run_as_non_root"`
	ServiceAccounts []serviceAccountCheck `json:"service_accounts"`
}

// serviceAccountCheck is one ServiceAccount the version's task pods would
// run as, the tasks that would, and what the lookup found. Only the number
// of its pull secrets is reported, not their names.
type serviceAccountCheck struct {
	Name             string   `json:"name"`
	Tasks            []string `json:"tasks"`
	State            string   `json:"state"`
	ImagePullSecrets *int     `json:"image_pull_secrets,omitempty"`
	Detail           string   `json:"detail,omitempty"`
}

// taskPodReporter builds the task_pods field of a dry run answer.
type taskPodReporter struct {
	settings TaskPodSettings
	logger   *slog.Logger
}

// report says what the pods of spec's tasks would run as: on a server that
// starts no task pods, nothing, and otherwise each ServiceAccount, sorted
// by name, looked up once.
func (r taskPodReporter) report(ctx context.Context, spec *domain.DAGSpec) *taskPodsReport {
	out := &taskPodsReport{ServiceAccounts: []serviceAccountCheck{}}
	if !r.settings.Enabled {
		return out
	}
	out.RunAsNonRoot = r.settings.RunAsNonRoot
	byName := map[string]*serviceAccountCheck{}
	for _, task := range spec.Tasks {
		name := r.accountOf(task)
		check, ok := byName[name]
		if !ok {
			check = &serviceAccountCheck{Name: name}
			byName[name] = check
		}
		check.Tasks = append(check.Tasks, task.TaskID)
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	slices.Sort(names)
	ctx, cancel := context.WithTimeout(ctx, serviceAccountLookupTimeout)
	defer cancel()
	for i, name := range names {
		check := byName[name]
		r.lookUp(ctx, check, i < maxServiceAccountLookups)
		out.ServiceAccounts = append(out.ServiceAccounts, *check)
	}
	return out
}

// accountOf is the ServiceAccount task's pod runs as, as dispatch decides
// it: the task's own, else the server's default, else the namespace's.
func (r taskPodReporter) accountOf(task domain.TaskSpec) string {
	if task.Execution != nil && task.Execution.ServiceAccount != "" {
		return task.Execution.ServiceAccount
	}
	if r.settings.DefaultServiceAccount != "" {
		return r.settings.DefaultServiceAccount
	}
	return namespaceDefaultServiceAccount
}

// lookUp fills check with what the reader finds, when allowed. A name no
// ServiceAccount can carry is missing without a lookup.
func (r taskPodReporter) lookUp(ctx context.Context, check *serviceAccountCheck, allowed bool) {
	check.State = serviceAccountUnchecked
	switch {
	case !validServiceAccountName(check.Name):
		check.State = serviceAccountMissing
		check.Detail = serviceAccountInvalidName
		return
	case r.settings.ServiceAccounts == nil:
		check.Detail = r.settings.Unread
		return
	case !allowed:
		check.Detail = serviceAccountTooMany
		return
	}
	n, found, err := r.settings.ServiceAccounts.ImagePullSecrets(ctx, check.Name)
	switch {
	case err != nil:
		r.logger.WarnContext(ctx, "dry run: reading a task ServiceAccount", "service_account", logSafe(check.Name), "error", logSafe(err.Error()))
		check.Detail = serviceAccountLookupFailed
	case !found:
		check.State = serviceAccountMissing
	default:
		check.State = serviceAccountFound
		check.ImagePullSecrets = &n
	}
}
