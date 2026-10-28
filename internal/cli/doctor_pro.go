package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// doctorProbeDagID is the dag_id doctor's control plane check names: the
// check sends an empty spec, refused before any DAG is looked up.
const doctorProbeDagID = "dexaflow-doctor"

// doctorCheckTimeout bounds each of doctor's network checks, shorter than a
// deploy's, so doctor does not stall on a machine that is offline.
const doctorCheckTimeout = 5 * time.Second

// doctorBuilder is the builder whose credentials doctor's push check
// judges: deploy's default.
const doctorBuilder = "docker"

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
// control plane set (or only Lite's), it says how to set one. The token is
// the one a deploy sends, a saved session refreshed as deploy refreshes it
// (the only write); the push check cancels its upload.
func proDoctor(cmd *cobra.Command) []doctorLine {
	choice, err := resolveServer(cmd, "")
	if err != nil {
		return []doctorLine{failLine("pro checks", err)}
	}
	if choice.Target == nil && (choice.Implicit || choice.Lite) {
		return []doctorLine{{label: "pro checks", text: "skipped: no target set (dexaflow target add <name> <url>, then dexaflow target use <name>)"}}
	}
	lines := []doctorLine{{label: "pro target", text: fmt.Sprintf("%s (%s)", choice.URL, choice.Source)}}
	ctx, cancel := context.WithTimeout(cmdContext(cmd), doctorCheckTimeout)
	defer cancel()
	_, token, err := resolveServerChoiceToken(cmd, "", "")
	if err != nil {
		return append(lines, failLine("control plane", err))
	}
	var notes bytes.Buffer
	if _, aerr := checkServerAccess(ctx, &notes, choice.URL, token, doctorProbeDagID, nil); aerr != nil {
		lines = append(lines, failLine("control plane", aerr))
	} else {
		lines = append(lines, doctorLine{label: "control plane", text: choice.URL + " answers, the token may deploy"})
	}
	if n := strings.TrimSpace(notes.String()); n != "" {
		lines = append(lines, doctorLine{text: n})
	}
	return append(lines, doctorRegistry(cmd, choice.URL, token))
}

// doctorRegistry is the push check of the DAG project in the current
// folder, with the registry deploy would push to, the deploy defaults of the
// cluster at serverURL included.
func doctorRegistry(cmd *cobra.Command, serverURL, token string) doctorLine {
	if !isProjectDir(".") {
		return doctorLine{label: "registry", text: "not checked: run doctor in a DAG project to check the push to its repository"}
	}
	cfg, err := loadDeployProject(".")
	if err != nil {
		return failLine("registry", err)
	}
	cluster := readClusterLayer(cmdContext(cmd), cmd.ErrOrStderr(), serverURL, token)
	if _, serr := settleDeployConfig(cmd, ".", cfg, cluster); serr != nil {
		return failLine("registry", serr)
	}
	ctx, cancel := context.WithTimeout(cmdContext(cmd), doctorCheckTimeout)
	defer cancel()
	res, err := registryPushCheck(ctx, registryFor(ctx, cfg), composeImageRef(cfg.Registry.URL, cfg.Registry.ImageName, "latest"), doctorBuilder)
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
