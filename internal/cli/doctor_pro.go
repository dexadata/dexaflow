package cli

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// doctorProbeDagID is the dag_id doctor's control plane check names: the
// check sends an empty spec, refused before any DAG is looked up.
const doctorProbeDagID = "dexaflow-doctor"

// doctorLine is one line of doctor's Pro section: its label, what the check
// found, and whether that is a refusal.
type doctorLine struct {
	label, text string
	fail        bool
}

// failLine is the doctorLine of a refusal err.
func failLine(label string, err error) doctorLine {
	return doctorLine{label: label, text: strings.TrimPrefix(err.Error(), "preflight: "), fail: true}
}

// proDoctor runs the deploy preflight's checks for the control plane in use
// (#1705): the server answers and the token may deploy, and, run in a DAG
// project, the registry accepts a push to the project's repository. With no
// control plane set (or only Lite's), it says how to set one. It writes
// nothing: the token is not refreshed, and the push check cancels its
// upload.
func proDoctor(cmd *cobra.Command) []doctorLine {
	choice, err := resolveServer(cmd, "")
	if err != nil {
		return []doctorLine{failLine("pro checks", err)}
	}
	if choice.Target == nil && (choice.Implicit || choice.Lite) {
		return []doctorLine{{label: "pro checks", text: "skipped: no target set (dexaflow target add <name> <url>, then dexaflow target use <name>)"}}
	}
	lines := []doctorLine{{label: "pro target", text: fmt.Sprintf("%s (%s)", choice.URL, choice.Source)}}
	token, _ := pickToken(choice, "")
	ctx := cmdContext(cmd)
	var notes bytes.Buffer
	if _, aerr := checkServerAccess(ctx, &notes, choice.URL, token, doctorProbeDagID, nil); aerr != nil {
		lines = append(lines, failLine("control plane", aerr))
	} else {
		lines = append(lines, doctorLine{label: "control plane", text: choice.URL + " answers, the token may deploy"})
	}
	if n := strings.TrimSpace(notes.String()); n != "" {
		lines = append(lines, doctorLine{text: n})
	}
	return append(lines, doctorRegistry(cmd))
}

// doctorRegistry is the push check of the DAG project in the current
// folder, with the registry deploy would push to.
func doctorRegistry(cmd *cobra.Command) doctorLine {
	if !isProjectDir(".") {
		return doctorLine{label: "registry", text: "not checked: run doctor in a DAG project to check the push to its repository"}
	}
	cfg, _, err := loadDeployConfig(cmd, ".")
	if err != nil {
		return failLine("registry", err)
	}
	res, err := registryPushCheck(cmdContext(cmd), composeImageRef(cfg.Registry.URL, cfg.Registry.ImageName, "latest"))
	switch {
	case err != nil:
		return failLine("registry", err)
	case res.note != "":
		return doctorLine{label: "registry", text: "note: " + res.note}
	}
	return doctorLine{label: "registry", text: res.ok}
}

// renderProDoctor writes the Pro section, each line under its label and
// the continuation lines of a message indented under the text.
func renderProDoctor(w io.Writer, lines []doctorLine) {
	_, _ = io.WriteString(w, "\n") //nolint:errcheck // best-effort terminal report
	for _, l := range lines {
		text := strings.ReplaceAll(l.text, "\n", "\n                ")
		if l.fail {
			text = "FAIL " + text
		}
		_, _ = fmt.Fprintf(w, "  %-13s %s\n", l.label, text) //nolint:errcheck // best-effort terminal report
	}
}
