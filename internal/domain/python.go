package domain

import (
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"
)

// PythonDeprecation records why a published Python line is on its way out and
// when it stops being published. It is read from the authoring schema rather
// than declared in Go so that the enum and the deprecation notes cannot drift
// apart: they are two fields of the same object.
type PythonDeprecation struct {
	// Version is the deprecated python_version value, e.g. "3.10".
	Version string `json:"-"`
	// EOL is the upstream CPython end-of-life date (YYYY-MM-DD). After it,
	// docker-library/python stops rebuilding the line and the base image stops
	// receiving OS security updates.
	EOL string `json:"eol"`
	// RemoveAfter is the date after which the release stops publishing a base
	// image for this line (YYYY-MM-DD).
	RemoveAfter string `json:"remove_after"`
	// Replacement is the version an author should move to.
	Replacement string `json:"replacement"`
	// Reason is the human-readable justification, quoted verbatim in the CLI
	// warning so the author gets the argument and not just a verdict.
	Reason string `json:"reason"`
}

// pythonVersionSchema is the shape of the schema's python_version subschema that
// this package cares about. Everything else in it (type, description) is the
// validator's business.
type pythonVersionSchema struct {
	Enum    []string `json:"enum"`
	Default string   `json:"default"`
	// The key is spelled by the JSON Schema document, not by us: `x-` is the
	// conventional prefix for a vendor extension keyword, and a validator must be
	// free to ignore it.
	Deprecations map[string]PythonDeprecation `json:"x-leoflow-python-deprecations"` //nolint:tagliatelle // JSON Schema vendor-extension keyword; the name is fixed by the schema document
}

// pythonSupport is parsed once. A malformed schema is a build-time defect, not a
// runtime condition, so the error is carried and surfaced by the accessors'
// callers rather than swallowed.
var pythonSupport = sync.OnceValues(loadPythonSupport)

func loadPythonSupport() (pythonVersionSchema, error) {
	var doc struct {
		Properties struct {
			PythonVersion pythonVersionSchema `json:"python_version"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(leoflowSchemaJSON, &doc); err != nil {
		return pythonVersionSchema{}, fmt.Errorf("parsing dexaflow.yaml schema: %w", err)
	}
	p := doc.Properties.PythonVersion
	if len(p.Enum) == 0 {
		return pythonVersionSchema{}, fmt.Errorf("dexaflow.yaml schema declares no python_version enum")
	}
	// A default that is not selectable, or one that is on its way out, would hand
	// every author who writes no python_version at all the version we are asking
	// them to leave. ApplyDefaults hard-codes the same string; the two are tied
	// together by TestApplyDefaultsPythonVersionIsSupported.
	if !slices.Contains(p.Enum, p.Default) {
		return pythonVersionSchema{}, fmt.Errorf(
			"dexaflow.yaml schema: python_version default %q is not in the enum %v", p.Default, p.Enum)
	}
	if _, deprecated := p.Deprecations[p.Default]; deprecated {
		return pythonVersionSchema{}, fmt.Errorf(
			"dexaflow.yaml schema: python_version default %q is deprecated — "+
				"move the default to a supported line before deprecating it", p.Default)
	}
	for v, d := range p.Deprecations {
		if !slices.Contains(p.Enum, v) {
			return pythonVersionSchema{}, fmt.Errorf(
				"dexaflow.yaml schema deprecates python_version %q, which is not in the enum %v — "+
					"a deprecation note for a version nobody can select is a note nobody will read", v, p.Enum)
		}
		if _, err := time.Parse(time.DateOnly, d.RemoveAfter); err != nil {
			return pythonVersionSchema{}, fmt.Errorf(
				"dexaflow.yaml schema: python_version %q has remove_after %q, want YYYY-MM-DD", v, d.RemoveAfter)
		}
	}
	return p, nil
}

// SupportedPythonVersions returns the python_version values the project
// publishes a task base image for, in schema order (ascending). The slice is a
// copy: callers may sort or filter it.
//
// This is the one list. scripts/check-python-runtime-matrix.sh asserts that the
// release matrix, the Makefile target, the runtime Dockerfile and the
// configuration reference all agree with it, because a published image nobody
// can select — and a selectable version nobody publishes — both surface as a
// `docker pull` 404 inside the user's own build.
func SupportedPythonVersions() ([]string, error) {
	p, err := pythonSupport()
	if err != nil {
		return nil, err
	}
	return append([]string(nil), p.Enum...), nil
}

// DeprecatedPythonVersion reports the deprecation record for v, if the schema
// carries one. The second result is false for a version that is fine.
func DeprecatedPythonVersion(v string) (PythonDeprecation, bool) {
	p, err := pythonSupport()
	if err != nil {
		return PythonDeprecation{}, false
	}
	d, ok := p.Deprecations[v]
	if !ok {
		return PythonDeprecation{}, false
	}
	d.Version = v
	return d, true
}
