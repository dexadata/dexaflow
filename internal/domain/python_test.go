package domain

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestSupportedPythonVersionsMatchesSchemaEnum pins the accessor to the schema
// the validator actually enforces. If the two ever came from different places a
// `python_version` could pass Validate() and still have no published base image
// — the failure would then surface as a `docker pull` 404 inside the user's
// build, which is the furthest possible point from the cause.
func TestSupportedPythonVersionsMatchesSchemaEnum(t *testing.T) {
	got, err := SupportedPythonVersions()
	if err != nil {
		t.Fatalf("SupportedPythonVersions: %v", err)
	}

	var doc struct {
		Properties struct {
			PythonVersion struct {
				Enum []string `json:"enum"`
			} `json:"python_version"`
		} `json:"properties"`
	}
	if uerr := json.Unmarshal(leoflowSchemaJSON, &doc); uerr != nil {
		t.Fatalf("unmarshal schema: %v", uerr)
	}
	if !slices.Equal(got, doc.Properties.PythonVersion.Enum) {
		t.Fatalf("SupportedPythonVersions() = %v, schema enum = %v", got, doc.Properties.PythonVersion.Enum)
	}
	if len(got) == 0 {
		t.Fatal("no supported Python versions — the schema enum must never be empty")
	}
}

// TestSupportedPythonVersionsIsACopy guards the accessor from handing callers a
// window into the parsed-once schema. A caller that sorts or appends to the
// result must not mutate what every later caller sees.
func TestSupportedPythonVersionsIsACopy(t *testing.T) {
	first, err := SupportedPythonVersions()
	if err != nil {
		t.Fatalf("SupportedPythonVersions: %v", err)
	}
	original := slices.Clone(first)
	for i := range first {
		first[i] = "mutated"
	}
	second, err := SupportedPythonVersions()
	if err != nil {
		t.Fatalf("SupportedPythonVersions: %v", err)
	}
	if !slices.Equal(second, original) {
		t.Fatalf("mutating the returned slice changed a later call: got %v, want %v", second, original)
	}
}

// TestDeprecatedPythonVersionIsSelectableAndDated asserts every deprecation note
// describes a version an author can actually write, and carries the two dates
// the CLI warning quotes. A note for a version that is not in the enum is a note
// nobody will ever be shown.
func TestDeprecatedPythonVersionIsSelectableAndDated(t *testing.T) {
	versions, err := SupportedPythonVersions()
	if err != nil {
		t.Fatalf("SupportedPythonVersions: %v", err)
	}
	deprecated := 0
	for _, v := range versions {
		d, ok := DeprecatedPythonVersion(v)
		if !ok {
			continue
		}
		deprecated++
		if d.Version != v {
			t.Errorf("DeprecatedPythonVersion(%q).Version = %q", v, d.Version)
		}
		for name, date := range map[string]string{"eol": d.EOL, "remove_after": d.RemoveAfter} {
			if _, perr := time.Parse(time.DateOnly, date); perr != nil {
				t.Errorf("python %s: %s = %q, want YYYY-MM-DD", v, name, date)
			}
		}
		if d.Reason == "" {
			t.Errorf("python %s: deprecation carries no reason — the warning would state a verdict with no argument", v)
		}
		if !slices.Contains(versions, d.Replacement) {
			t.Errorf("python %s: replacement %q is not itself a supported version", v, d.Replacement)
		}
		if _, alsoDeprecated := DeprecatedPythonVersion(d.Replacement); alsoDeprecated {
			t.Errorf("python %s: replacement %q is itself deprecated", v, d.Replacement)
		}
	}
	if deprecated == len(versions) {
		t.Fatal("every published Python line is deprecated — there is nowhere to send an author")
	}
}

// TestDeprecatedPythonVersionUnknown keeps the negative path honest: a version
// with no note, and a string that is not a version at all, both report false
// rather than a zero-valued record that reads as "deprecated with no details".
func TestDeprecatedPythonVersionUnknown(t *testing.T) {
	for _, v := range []string{"", "3.99", "not-a-version"} {
		if d, ok := DeprecatedPythonVersion(v); ok {
			t.Errorf("DeprecatedPythonVersion(%q) = %+v, true; want false", v, d)
		}
	}
}

// TestApplyDefaultsPythonVersionIsSupported ties the hard-coded default in
// ApplyDefaults to the schema. They are two statements of one fact and the
// schema is the one that is enforced.
func TestApplyDefaultsPythonVersionIsSupported(t *testing.T) {
	c := &LeoflowConfig{DagID: "d"}
	c.ApplyDefaults()
	versions, err := SupportedPythonVersions()
	if err != nil {
		t.Fatalf("SupportedPythonVersions: %v", err)
	}
	if !slices.Contains(versions, c.PythonVersion) {
		t.Fatalf("ApplyDefaults set python_version %q, not in the schema enum %v", c.PythonVersion, versions)
	}
	if d, ok := DeprecatedPythonVersion(c.PythonVersion); ok {
		t.Fatalf("ApplyDefaults defaults to the deprecated python_version %q (%s); move the default first",
			c.PythonVersion, d.Reason)
	}
}

// TestValidateAcceptsEverySupportedPythonVersion closes the loop from the other
// side: the enum this package exports must be exactly what Validate() lets
// through, so a version the release publishes can never be rejected by the CLI.
func TestValidateAcceptsEverySupportedPythonVersion(t *testing.T) {
	versions, err := SupportedPythonVersions()
	if err != nil {
		t.Fatalf("SupportedPythonVersions: %v", err)
	}
	for _, v := range versions {
		c := &LeoflowConfig{DagID: "d", PythonVersion: v}
		c.ApplyDefaults()
		c.PythonVersion = v
		if verr := c.Validate(); verr != nil {
			t.Errorf("python_version %q is published but Validate() rejects it: %v", v, verr)
		}
	}
	c := &LeoflowConfig{DagID: "d", PythonVersion: "3.9"}
	c.ApplyDefaults()
	c.PythonVersion = "3.9"
	if verr := c.Validate(); verr == nil {
		t.Error("Validate() accepted python_version 3.9, for which no base image is published")
	}
}

// TestRetiredPythonVersionIsRecordedAndUnselectable covers a line that is no
// longer published (#1447). It must be out of the enum, so no new build can
// select it, and still carry its record, so the CLI can say why the value is
// refused and where to go instead.
func TestRetiredPythonVersionIsRecordedAndUnselectable(t *testing.T) {
	versions, err := SupportedPythonVersions()
	if err != nil {
		t.Fatalf("SupportedPythonVersions: %v", err)
	}
	r, ok := RetiredPythonVersion("3.10")
	if !ok {
		t.Fatal("3.10 is not recorded as retired")
	}
	if r.Version != "3.10" {
		t.Errorf("RetiredPythonVersion(3.10).Version = %q", r.Version)
	}
	if slices.Contains(versions, "3.10") {
		t.Errorf("3.10 is retired but still in the enum %v", versions)
	}
	if !slices.Contains(versions, r.Replacement) {
		t.Errorf("replacement %q is not a supported version", r.Replacement)
	}
	if _, deprecated := DeprecatedPythonVersion(r.Replacement); deprecated {
		t.Errorf("replacement %q is itself deprecated", r.Replacement)
	}
	for _, v := range versions {
		if _, retired := RetiredPythonVersion(v); retired {
			t.Errorf("python %s is both published and retired", v)
		}
	}
	for _, v := range []string{"", "3.99", "3.11"} {
		if _, retired := RetiredPythonVersion(v); retired {
			t.Errorf("RetiredPythonVersion(%q) = true, want false", v)
		}
	}
}

// TestValidateNamesTheReplacementForARetiredPythonVersion asserts the refusal
// of a retired line says what happened and what to write instead. The bare
// schema error ("value must be one of") reads as a typo to an author whose
// project built fine on the previous release.
func TestValidateNamesTheReplacementForARetiredPythonVersion(t *testing.T) {
	c := &LeoflowConfig{DagID: "d"}
	c.ApplyDefaults()
	c.PythonVersion = "3.10"
	verr := c.Validate()
	if verr == nil {
		t.Fatal("Validate() accepted the retired python_version 3.10")
	}
	for _, want := range []string{"python_version 3.10", "no longer published", `python_version: "3.11"`} {
		if !strings.Contains(verr.Error(), want) {
			t.Errorf("error %q does not contain %q", verr, want)
		}
	}
}
