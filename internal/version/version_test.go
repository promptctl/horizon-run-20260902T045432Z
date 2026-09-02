package version

import (
	"runtime/debug"
	"testing"
)

func TestNormalizeDropsTheModuleVersionPrefix(t *testing.T) {
	// A Go module version always carries a leading "v"; the spec's version
	// string does not (the reference build reports "Mackup 0.11.1").
	tests := map[string]string{
		"v0.11.1":                       "0.11.1",
		"0.11.1":                        "0.11.1",
		"v0.0.0-20260902050000-32eaf47": "0.0.0-20260902050000-32eaf47",
		"":                              "",
	}
	for in, want := range tests {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStringNormalizesTheStampedValue(t *testing.T) {
	original := value
	t.Cleanup(func() { value = original })

	value = "v1.2.3"
	if got := String(); got != "1.2.3" {
		t.Errorf("String() = %q, want %q", got, "1.2.3")
	}
	if got := Banner(); got != "Mackup 1.2.3" {
		t.Errorf("Banner() = %q, want %q", got, "Mackup 1.2.3")
	}
}

func TestStringFallsBackForAnUninstalledTree(t *testing.T) {
	original := value
	t.Cleanup(func() { value = original })

	value = ""
	if got := String(); got != Fallback {
		t.Errorf("String() = %q, want the fallback token %q", got, Fallback)
	}
}

func TestFromBuildInfoTakesTheVersionOfAnInstalledBuild(t *testing.T) {
	// `go install github.com/promptctl/macklebox/cmd/mackup@v0.1.0` builds from
	// the module cache: the module version is real and no vcs.* setting is
	// present. Observed on go1.25.7.
	info := &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}
	if got := fromBuildInfo(info); got != "0.1.0" {
		t.Errorf("fromBuildInfo(installed) = %q, want %q", got, "0.1.0")
	}
}

func TestFromBuildInfoRejectsAVersionStampedFromAWorkingTree(t *testing.T) {
	// A build from a checkout is an uninstalled tree, so it is owed the
	// fallback token -- even though Go 1.24 and later label it with a
	// pseudo-version derived from the commit rather than with "(devel)". The
	// vcs.* settings are what identify it. Both shapes were observed on
	// go1.25.7: `go build` in the repository, and the same build with the
	// working tree dirty.
	for name, info := range map[string]*debug.BuildInfo{
		"pseudo-version from a clean checkout": {
			Main: debug.Module{Version: "v0.0.0-20260902061313-36c0e57a6b95"},
			Settings: []debug.BuildSetting{
				{Key: "vcs", Value: "git"},
				{Key: "vcs.revision", Value: "36c0e57a6b95178"},
				{Key: "vcs.modified", Value: "false"},
			},
		},
		"pseudo-version from a dirty checkout": {
			Main: debug.Module{Version: "v0.0.0-20260902061304-da54d01d3c9b+dirty"},
			Settings: []debug.BuildSetting{
				{Key: "vcs", Value: "git"},
				{Key: "vcs.revision", Value: "da54d01d3c9b178"},
				{Key: "vcs.modified", Value: "true"},
			},
		},
		"unstamped tree, where the toolchain declined to read the repository": {
			Main: debug.Module{Version: "(devel)"},
		},
		"no version at all": {},
	} {
		if got := fromBuildInfo(info); got != "" {
			t.Errorf("fromBuildInfo(%s) = %q, want \"\" so the fallback token is used", name, got)
		}
	}
}

// withBuildInfo makes String() read the build info of a program this test is
// not running, and restores both knobs afterwards.
func withBuildInfo(t *testing.T, info *debug.BuildInfo) {
	t.Helper()
	originalValue, originalRead := value, readBuildInfo
	t.Cleanup(func() { value, readBuildInfo = originalValue, originalRead })
	readBuildInfo = func() (*debug.BuildInfo, bool) { return info, true }
}

func TestAnExplicitStampOutranksTheModulesOwnVersion(t *testing.T) {
	// The only shape in which precedence is observable at all. A working-tree
	// build yields "" from fromBuildInfo, so a stamp has nothing to outrank
	// there and asserting on it proves nothing -- which is exactly what the
	// case this replaces did, twice over: it stayed green both with the
	// precedence inverted and with fromBuildInfo disconnected.
	withBuildInfo(t, &debug.BuildInfo{Main: debug.Module{Version: "v0.9.0"}})

	// Unstamped, the module's version is the answer. Without this the case
	// could pass against build info that was never consulted.
	value = ""
	if got := String(); got != "0.9.0" {
		t.Fatalf("String() = %q unstamped, want the module version %q; the assertion below would prove nothing", got, "0.9.0")
	}

	value = "v0.11.1"
	if got := String(); got != "0.11.1" {
		t.Errorf("String() = %q, want the stamp %q to outrank the module version %q", got, "0.11.1", "0.9.0")
	}
}

func TestAnExplicitStampSurvivesAWorkingTreeBuild(t *testing.T) {
	// `make build VERSION=x.y.z` from a checkout: the build carries both the
	// stamp and the VCS settings that make fromBuildInfo report nothing. The
	// release number must still come out.
	//
	// The regression this guards is a plausible one: appspec/00 says an
	// uninstalled tree reports "unknown", and reading that as a rule about
	// the program rather than about the module's metadata puts a provenance
	// check ahead of the stamp -- which would make every release build made
	// from a checkout, which is how this project makes them, report unknown.
	withBuildInfo(t, &debug.BuildInfo{
		Main:     debug.Module{Version: "v0.0.0-20260902061304-da54d01d3c9b"},
		Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "da54d01d3c9b"}},
	})

	value = ""
	if got := String(); got != Fallback {
		t.Fatalf("String() = %q for an unstamped working-tree build, want %q; the assertion below would prove nothing", got, Fallback)
	}

	value = "v0.11.1"
	if got := String(); got != "0.11.1" {
		t.Errorf("String() = %q, want the stamped %q even though the build came from a working tree", got, "0.11.1")
	}
}
