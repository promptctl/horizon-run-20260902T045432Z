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
		if v := fromBuildInfo(bi); v != "" {
			return v
		}
	}
	return Fallback
}

// fromBuildInfo reads the package version out of the build info, or returns ""
// when the build carries none and the fallback token is owed.
//
// A build made from a working tree is an uninstalled tree whatever it calls
// itself, and since Go 1.24 such a build is no longer labelled "(devel)": the
// toolchain derives a version from the checkout itself. That is a build
// identity, not a package version -- precisely the case
// appspec/00-overview.md says must report the literal token "unknown", since
// the metadata it names is "installed package metadata" and there is none.
//
// It takes two shapes, and discarding BOTH is deliberate. The obvious one is
// the pseudo-version of an untagged commit,
// "0.0.0-20260902061304-da54d01d3c9b+dirty". The one that reads like a bug is
// a clean checkout sitting on a release tag, which stamps the tag itself:
// `go build` at v0.11.1 yields Main.Version "v0.11.1" alongside vcs.revision,
// observed on go1.25.7. Reporting 0.11.1 there would still be wrong -- the
// tree is uninstalled, and the number would come from whatever tag the
// checkout happens to sit on rather than from a package. A release build gets
// its version the way the Makefile gives it one, through -X, which arrives as
// the linker stamp above and never reaches here.
//
// The two are told apart by provenance rather than by the shape of the string:
// a build from a checkout carries vcs.* build settings, and one installed from
// the module cache (go install <pkg>@v0.1.0) does not. Both were observed
// directly on go1.25.7 before this was written.
//
// Whether the toolchain stamps VCS information at all is environment-
// dependent (-buildvcs defaults to auto, and it declines, without saying so,
// when it cannot read the repository), which is why this cannot be decided
// from the shape of Main.Version: the same source tree yields "(devel)" on one
// machine and a pseudo-version on another.
func fromBuildInfo(bi *debug.BuildInfo) string {
	for _, setting := range bi.Settings {
		if setting.Key == "vcs.revision" {
			return ""
		}
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return normalize(v)
	}
	return ""
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
