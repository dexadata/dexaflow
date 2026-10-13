package build

import (
	"encoding/json"
	"strings"
)

// manifestList is the part of an image index (OCI) or manifest list (Docker)
// that says which platforms it holds. A single-image manifest has no
// manifests and so covers no list of platforms.
type manifestList struct {
	Manifests []struct {
		Platform struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
			Variant      string `json:"variant"`
		} `json:"platform"`
	} `json:"manifests"`
}

// missingPlatforms returns the requested platforms the manifest list in raw
// does not hold, all of them when raw is not a list. A requested platform
// with a variant needs that variant (arm64 with none counts as v8); one
// without matches any variant.
// Platforms compare without regard to case, as docker compares them.
func missingPlatforms(raw []byte, platforms []string) []string {
	var list manifestList
	if json.Unmarshal(raw, &list) != nil {
		return platforms
	}
	var missing []string
	for _, p := range platforms {
		if !listHolds(list, p) {
			missing = append(missing, p)
		}
	}
	return missing
}

// listHolds reports whether list has an image for platform.
func listHolds(list manifestList, platform string) bool {
	parts := strings.SplitN(strings.ToLower(platform), "/", 3)
	for _, m := range list.Manifests {
		got := m.Platform
		if !strings.EqualFold(got.OS, parts[0]) || !strings.EqualFold(got.Architecture, parts[1]) {
			continue
		}
		if len(parts) < 3 || variantOf(parts[1], got.Variant) == parts[2] {
			return true
		}
	}
	return false
}

// variantOf normalizes a listed variant as containerd does: arm64 with no
// variant is arm64/v8.
func variantOf(arch, variant string) string {
	v := strings.ToLower(variant)
	if v == "" && arch == "arm64" {
		return "v8"
	}
	return v
}

// isIndex reports whether raw is an image index or manifest list: it lists
// manifests rather than layers.
func isIndex(raw []byte) bool {
	var list manifestList
	return json.Unmarshal(raw, &list) == nil && len(list.Manifests) > 0
}
