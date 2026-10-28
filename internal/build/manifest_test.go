package build

import (
	"strings"
	"testing"
)

func TestMissingPlatforms(t *testing.T) {
	cases := []struct {
		name, raw string
		want      []string
		missing   string
	}{
		{"every platform", indexOf("linux/amd64", "linux/arm64"), []string{"linux/amd64", "linux/arm64"}, ""},
		{"case", indexOf("linux/amd64"), []string{"Linux/AMD64"}, ""},
		// containerd treats arm64 with no variant as arm64/v8.
		{"arm64 without a variant is v8", indexOf("linux/arm64"), []string{"linux/arm64/v8"}, ""},
		{"other arm64 variant", indexOf("linux/arm64/v7"), []string{"linux/arm64/v8"}, "linux/arm64/v8"},
		{"no variant asked", indexOf("linux/arm/v7"), []string{"linux/arm"}, ""},
		{"missing one", indexOf("linux/arm64"), []string{"linux/amd64", "linux/arm64"}, "linux/amd64"},
		{"single image", `{"schemaVersion":2,"layers":[]}`, []string{"linux/amd64"}, "linux/amd64"},
		{"not json", "nope", []string{"linux/amd64", "linux/arm64"}, "linux/amd64,linux/arm64"},
		// A list that decodes in part is not trusted in part.
		{"partly malformed", `{"manifests":[{"platform":{"os":"linux","architecture":"amd64"}},{"platform":"bad"}]}`, []string{"linux/amd64"}, "linux/amd64"},
	}
	for _, c := range cases {
		if got := strings.Join(missingPlatforms([]byte(c.raw), c.want), ","); got != c.missing {
			t.Errorf("%s: missing = %q, want %q", c.name, got, c.missing)
		}
	}
}
