package cli

import "testing"

func TestReleaseBaseTag(t *testing.T) {
	cases := map[string]string{
		"v0.4.2":                  "v0.4.2",      // clean release tag → pin
		"v0.4.3-rc.1":             "v0.4.3-rc.1", // rc tag is still a release
		"v1.2.3-beta.4":           "v1.2.3-beta.4",
		"v0.4.2-9-gabc1234":       "", // git describe (commits after tag) → moving
		"v0.4.2-9-gabc1234-dirty": "", // describe + dirty
		"v0.4.2-dirty":            "", // dirty tree
		"dev":                     "", // default source build
		"":                        "", // unset
	}
	for in, want := range cases {
		if got := releaseBaseTag(in); got != want {
			t.Errorf("releaseBaseTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBaseImageRef(t *testing.T) {
	const repo = "ghcr.io/dexadata/leoflow-runtime"
	cases := map[string]string{
		// GoReleaser stamps the CLI version WITHOUT the leading v; the published base
		// tag ALWAYS has it. Both forms must resolve to the same py<ver>-v<X> tag.
		"0.4.2":      repo + ":py3.11-v0.4.2",
		"v0.4.2":     repo + ":py3.11-v0.4.2",
		"0.4.3-rc.1": repo + ":py3.11-v0.4.3-rc.1",
		// dev/dirty/describe → moving tag (no versioned base is published for them).
		"0.4.2-9-gabc1234": repo + ":py3.11",
		"v0.4.2-dirty":     repo + ":py3.11",
		"dev":              repo + ":py3.11",
		"":                 repo + ":py3.11",
	}
	for ver, want := range cases {
		if got := baseImageRef(repo, "3.11", ver); got != want {
			t.Errorf("baseImageRef(_, 3.11, %q) = %q, want %q", ver, got, want)
		}
	}
}
