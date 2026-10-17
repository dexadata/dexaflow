package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The task pod step of the preflight (#1889): two failures that otherwise
// show only when a task pod starts. The server's dry run reports the
// ServiceAccount each task's pod would run as, looked up in the task
// namespace, and whether task pods run as non-root; the CLI reads the user
// the project's own Dockerfile ends with. What stops the deploy names the
// fix; what cannot be decided is a note.

// taskPodsAnswer is the task_pods field of a dry run answer: absent from an
// older server's.
type taskPodsAnswer struct {
	RunAsNonRoot    bool                 `json:"run_as_non_root"`
	ServiceAccounts []taskServiceAccount `json:"service_accounts"`
}

// taskServiceAccount is one ServiceAccount of a dry run's task_pods.
type taskServiceAccount struct {
	Name             string   `json:"name"`
	Tasks            []string `json:"tasks"`
	State            string   `json:"state"`
	ImagePullSecrets *int     `json:"image_pull_secrets"`
	Detail           string   `json:"detail"`
}

// namespaceDefaultServiceAccount is the ServiceAccount Kubernetes creates
// in every namespace, shortly after the namespace itself.
const namespaceDefaultServiceAccount = "default"

// maxTasksNamed bounds the tasks a line names for one ServiceAccount.
const maxTasksNamed = 5

// nonRootUserExample is the USER the fixes name: the numeric non-root id
// the runtime base image runs as.
const nonRootUserExample = "USER 65532:65532"

// checkTaskServiceAccounts reports each ServiceAccount of pods: one that
// does not exist, or whose name no ServiceAccount can carry (the server's
// detail says so), stops the deploy, since the pods of its tasks would not
// be created; the namespace's default, which Kubernetes creates a moment
// after the namespace, is a note when it is missing; one with no pull secret, or one the server could not look
// up, is a note on warn; one with pull secrets is a line on out. Everything
// the server sent is printed through printable.
func checkTaskServiceAccounts(out, warn io.Writer, pods *taskPodsAnswer) error {
	if pods == nil {
		return nil
	}
	var missing []string
	for _, sa := range pods.ServiceAccounts {
		name, tasks := printable(sa.Name), namedTasks(sa.Tasks)
		var werr error
		switch {
		case sa.State == "missing" && sa.Name == namespaceDefaultServiceAccount:
			_, werr = fmt.Fprintf(warn, "note: the namespace's default ServiceAccount %q (tasks %s) was not found; Kubernetes creates it in every "+
				"namespace shortly after the namespace, so this is expected only right after the task namespace was created\n", name, tasks)
		case sa.State == "missing" && sa.Detail != "":
			missing = append(missing, fmt.Sprintf("%q (tasks %s): %s", name, tasks, printable(sa.Detail)))
		case sa.State == "missing":
			missing = append(missing, fmt.Sprintf("%q (tasks %s)", name, tasks))
		case sa.State == "found" && sa.ImagePullSecrets != nil && *sa.ImagePullSecrets > 0:
			_, werr = fmt.Fprintf(out, "preflight:  task ServiceAccount %q exists, with %d imagePullSecrets (tasks %s)\n", name, *sa.ImagePullSecrets, tasks)
		case sa.State == "found":
			_, werr = fmt.Fprintf(warn, "note: task ServiceAccount %q (tasks %s) carries no imagePullSecrets: a private registry is pulled then only "+
				"with the nodes' own credentials (IRSA, Workload Identity, a credential helper); otherwise the operator lists a pull secret in "+
				"taskServiceAccount.imagePullSecrets in the Helm values\n", name, tasks)
		default:
			_, werr = fmt.Fprintf(warn, "note: could not check task ServiceAccount %q (tasks %s): %s\n", name, tasks, printable(sa.Detail))
		}
		if werr != nil {
			return werr
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("preflight: task ServiceAccount %s does not exist in the server's task namespace, so these task pods would not be created.\n"+
		"  Set execution.service_account in dexaflow.yaml to a ServiceAccount that exists, or have the operator create it\n"+
		"  (taskServiceAccount.create and taskServiceAccount.name in the Helm values, for the default task ServiceAccount)",
		strings.Join(missing, ", "))
}

// namedTasks lists tasks for a line, at most maxTasksNamed of them.
func namedTasks(tasks []string) string {
	shown := make([]string, 0, min(len(tasks), maxTasksNamed))
	for _, t := range tasks[:min(len(tasks), maxTasksNamed)] {
		shown = append(shown, printable(t))
	}
	s := strings.Join(shown, ", ")
	if extra := len(tasks) - len(shown); extra > 0 {
		s += fmt.Sprintf(" and %d more", extra)
	}
	return s
}

// checkDockerfileUser reads the user the Dockerfile at path (shown as name)
// ends with, against nonRoot: whether the server runs task pods as
// non-root, nil when it did not say (an older server, or no dry run). When
// it does, a final USER of root or of a name stops the deploy, since the
// kubelet would refuse to start the pods, and a USER the preflight cannot
// read (none, or a build variable) is a note. When it did not say, root or
// a name is a note. When it does not, nothing is said.
func checkDockerfileUser(out, warn io.Writer, path, name string, nonRoot *bool) error {
	if nonRoot != nil && !*nonRoot {
		return nil
	}
	f, err := os.Open(path) //nolint:gosec // the project's own Dockerfile, which the build reads
	if err != nil {
		return fmt.Errorf("reading %s: %w", name, err)
	}
	defer f.Close() //nolint:errcheck // read only
	u, err := finalStageUser(f)
	if err != nil {
		return err
	}
	if nonRoot == nil {
		return noteUnknownNonRoot(warn, name, u)
	}
	var werr error
	switch u.kind {
	case userNonRoot:
		_, werr = fmt.Fprintf(out, "preflight:  %s runs as USER %s, a numeric non-root id\n", name, u.value)
	case userRoot:
		return fmt.Errorf("preflight: %s runs as root (USER %s, line %d), and task pods on this server run as non-root: the kubelet would refuse to start them.\n"+
			"  End the Dockerfile with a numeric non-root USER, such as:  %s\n"+
			"  (or the cluster's operator sets taskPodSecurity.runAsNonRoot=false)", name, u.value, u.line, nonRootUserExample)
	case userNamed:
		return fmt.Errorf("preflight: %s runs as a name (USER %s, line %d), and task pods on this server run as non-root: the kubelet cannot tell that a name is not root, so it would refuse to start them.\n"+
			"  Use the numeric id instead, such as:  %s", name, u.value, u.line, nonRootUserExample)
	case userUnset:
		_, werr = fmt.Fprintf(warn, "note: %s sets no USER in its final stage, so the base image decides; task pods on this server refuse one that runs as root. "+
			"End it with a numeric non-root USER, such as %s\n", name, nonRootUserExample)
	case userUnresolved:
		_, werr = fmt.Fprintf(warn, "note: %s runs as USER %s (line %d), which only the build resolves; task pods on this server start only a numeric non-root id\n",
			name, u.value, u.line)
	}
	return werr
}

// noteUnknownNonRoot is the note on a Dockerfile that ends as root or a
// name when the server did not say whether it runs task pods as non-root.
func noteUnknownNonRoot(warn io.Writer, name string, u dockerfileUser) error {
	var what string
	switch u.kind {
	case userRoot:
		what = "root"
	case userNamed:
		what = "a name"
	default:
		return nil
	}
	_, err := fmt.Fprintf(warn, "note: %s runs as %s (USER %s, line %d); if the server runs task pods as non-root (the default), they will not start. "+
		"End it with a numeric non-root USER, such as %s\n", name, what, u.value, u.line, nonRootUserExample)
	return err
}

// ownDockerfile is the project's own Dockerfile this deploy builds from,
// with its name as given, or "" when nothing is built from one: no build
// (--skip-build, source mode), or a Dockerfile generated from dexaflow.yaml.
// A build.dockerfile outside the project is left to the compile, which
// refuses it.
func (d *deployRun) ownDockerfile() (path, name string) {
	if d.o.skipBuild || d.sourceMode.on() {
		return "", ""
	}
	name, fromConfig := resolveDockerfileName(d.cmd, compileOptions{dockerfile: d.o.dockerfile}, d.cfg)
	if fromConfig && checkConfiguredDockerfile(d.dir, name) != nil {
		return "", ""
	}
	path = filepath.Join(d.dir, name)
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		return "", ""
	}
	return path, name
}

// checkTaskPods is the task pod step of the preflight, before the build,
// with pods as the dry run reported them (nil for none).
func (d *deployRun) checkTaskPods(pods *taskPodsAnswer) error {
	out, warn := d.cmd.OutOrStdout(), d.cmd.ErrOrStderr()
	serr := checkTaskServiceAccounts(out, warn, pods)
	path, name := d.ownDockerfile()
	if path == "" {
		return serr
	}
	var nonRoot *bool
	if pods != nil {
		nonRoot = &pods.RunAsNonRoot
	}
	return errors.Join(serr, checkDockerfileUser(out, warn, path, name, nonRoot))
}
