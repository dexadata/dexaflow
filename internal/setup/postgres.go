package setup

import "fmt"

// Pinned relocatable PostgreSQL build (theseus-rs/postgresql-binaries). These are
// the datastore Dexaflow Lite manages itself so it needs no Docker (Fase 2). The
// major is pinned to 16 to match the typical managed Postgres in Production.
// Bumping the version means refreshing every SHA-256 below from the release's
// per-asset .sha256 files.
const (
	pgVersion = "16.13.0"
	pgBaseURL = "https://github.com/theseus-rs/postgresql-binaries/releases/download"
)

// PostgresBuild is a relocatable PostgreSQL release asset and its checksum.
type PostgresBuild struct {
	URL     string
	SHA256  string
	Version string
}

// pgTriple maps a (GOOS, GOARCH, libc) host to the theseus-rs target triple. libc
// is honored only on linux; "" defaults to glibc. Mirrors pythonTriple, since both
// projects use Rust target-triple naming.
func pgTriple(goos, goarch, libc string) (string, error) {
	switch goos {
	case "darwin":
		switch goarch {
		case "arm64":
			return "aarch64-apple-darwin", nil
		case "amd64":
			return "x86_64-apple-darwin", nil
		}
	case "linux":
		suffix := "gnu"
		if libc == "musl" {
			suffix = "musl"
		}
		switch goarch {
		case "amd64":
			return "x86_64-unknown-linux-" + suffix, nil
		case "arm64":
			return "aarch64-unknown-linux-" + suffix, nil
		}
	}
	return "", fmt.Errorf("no relocatable PostgreSQL build for %s/%s (libc %q)", goos, goarch, libc)
}

// postgresSHA256 holds the pinned checksum for each supported triple (PG 16.13.0).
var postgresSHA256 = map[string]string{
	"aarch64-apple-darwin":       "770cb9985aa903a8207ec98a3dfe762bf40234486ee5950c3809ddf6b1c9f921",
	"x86_64-apple-darwin":        "cff3bdaae703162b36c7622d5a30486df498ad72c2b0dc4a62173ec0d17df29a",
	"x86_64-unknown-linux-gnu":   "bb8dba9ca0f04e5cc6e21a13a41a3f111e030ef6c3b41a872e32c6f9e5a39331",
	"aarch64-unknown-linux-gnu":  "d4fe7f1fd8762d837d4091438fb139321ea7cb11abce95f51616ae1434fffd77",
	"x86_64-unknown-linux-musl":  "7a2cfa7515765933d6b8c112f9dd68890147af6631fbc1dc5cabca7416ecd363",
	"aarch64-unknown-linux-musl": "f6348a9bf53b84d5995336f4a9f059fec2ae9cf2dc6ad0e800c8c0d0d20172d9",
}

// ResolvePostgres returns the pinned relocatable PostgreSQL asset for the host, or
// an error if the platform is unsupported (e.g. Windows, where WSL2 is the path).
func ResolvePostgres(goos, goarch, libc string) (PostgresBuild, error) {
	triple, err := pgTriple(goos, goarch, libc)
	if err != nil {
		return PostgresBuild{}, err
	}
	sha, ok := postgresSHA256[triple]
	if !ok {
		return PostgresBuild{}, fmt.Errorf("no pinned checksum for triple %q", triple)
	}
	asset := fmt.Sprintf("postgresql-%s-%s.tar.gz", pgVersion, triple)
	return PostgresBuild{
		URL:     fmt.Sprintf("%s/%s/%s", pgBaseURL, pgVersion, asset),
		SHA256:  sha,
		Version: pgVersion,
	}, nil
}
