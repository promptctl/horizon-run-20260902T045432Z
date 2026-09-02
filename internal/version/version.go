// Package version resolves the version string the program reports.
//
// appspec/00-overview.md "Provenance" makes this a contract: the version is
// the package's own version when the program is installed, and a stable
// fallback token otherwise. It is reported by --version and in the list
// trailer as "Mackup <version>".
package version

import (
	"runtime/debug"
	"strings"
)

// Fallback is the stable token reported when no package version is available
// (running from an uninstalled tree). appspec/00-overview.md names the literal
// token "unknown".
const Fallback = "unknown"

// value is stamped at link time by the build:
//
//	go build -ldflags "-X github.com/promptctl/macklebox/internal/version.value=1.2.3"
//
// When it is empty the version is recovered from the module build info.
var value string

// String returns the resolved version string.
func String() string {
	if value != "" {
		return normalize(value)
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return normalize(v)
		}
	}
	return Fallback
}

// normalize drops the "v" that a Go module version always carries
// (golang.org/ref/mod: a version "begins with the letter v"). The spec's
// version string does not: the reference build reports "Mackup 0.11.1". Both
// sources go through here so a stamped "v1.2.3" cannot reintroduce the prefix
// through the other door.
func normalize(v string) string {
	return strings.TrimPrefix(v, "v")
}

// Banner returns the exact line --version prints: "Mackup <version>".
func Banner() string {
	return "Mackup " + String()
}
