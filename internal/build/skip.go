package build

import "context"

// Existing is the skip path: the registry already holds the image for this
// build's content, so nothing is built or pushed and the image is pinned to
// the digest the registry returned when Select looked it up.
type Existing struct {
	// Pinned is what the registry holds under the image tag.
	Pinned Pinned
}

// Path is PathSkip.
func (Existing) Path() Path { return PathSkip }

// Describe names the path.
func (Existing) Describe() string { return string(PathSkip) }

// Build returns the image already in the registry, pinned.
func (e Existing) Build(_ context.Context, req Request) (Result, error) {
	return Result{Path: PathSkip, Image: req.Image, Platforms: req.Platforms, Pushed: true, Ref: e.Pinned.Ref}, nil
}
