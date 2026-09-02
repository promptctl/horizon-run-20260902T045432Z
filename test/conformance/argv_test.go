package conformance

import (
	"regexp"
	"strings"
	"testing"
)

// The argv boundary of appspec/02-invocation.md, observed at the process
// boundary: which forms are accepted, which are usage errors, which stream each
// answer lands on, and the exit code.

func TestHelpPrintsUsageToStdoutAndExitsZero(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		world := NewWorld(t)
		before := world.Snapshot()

		world.Run(flag).
			ExpectExit(0).
			ExpectStdout("Usage:").
			ExpectStdout("mackup [options] link install [<application>]").
			ExpectSilentStderr()

		// appspec/02: "No other action, no config read."
		world.ExpectUnchanged(before)
	}
}

func TestVersionPrintsTheMackupLineToStdoutAndExitsZero(t *testing.T) {
	world := NewWorld(t)
	before := world.Snapshot()

	result := world.Run("--version").ExpectExit(0).ExpectSilentStderr()
	if !regexp.MustCompile(`^Mackup \S+\n$`).MatchString(result.Stdout) {
		t.Errorf("stdout = %q, want a single \"Mackup <version>\" line", result.Stdout)
	}
	world.ExpectUnchanged(before)
}

func TestVersionReportsTheFallbackTokenForAnUninstalledBuild(t *testing.T) {
	// appspec/00-overview.md "Provenance": the version is the package's own
	// version when installed, and a stable fallback token otherwise. This is
	// the program exactly as `make build` produces it.
	NewWorld(t).Run("--version").
		ExpectExit(0).
		ExpectStdout("Mackup unknown")
}

func TestVersionReportsItsOwnVersionWhenTheBuildCarriesOne(t *testing.T) {
	world := NewWorld(t)
	world.UseStampedBinary()
	world.Run("--version").
		ExpectExit(0).
		ExpectStdout("Mackup " + stampedVersion)
}

func TestConflictingForceFlagsEmitTheLiteralLineAndExitOne(t *testing.T) {
	// appspec/07 "Error behavior summary" names this line as one of the three
	// literal tokens that are contract, matched by scripts and tests.
	const line = "Options --force and --force-no are mutually exclusive."

	for _, args := range [][]string{
		{"--force", "--force-no", "backup"},
		{"-f", "--force-no", "list"},
		{"--force-no", "-f", "link", "install", "vim"},
		{"-f", "--force-no"},
	} {
		world := NewWorld(t)
		before := world.Snapshot()

		world.Run(args...).
			ExpectExit(1).
			ExpectStderrLine(line).
			ExpectSilentStdout()

		// appspec/02: rejected "without reading config or performing any
		// action", before config is loaded.
		world.ExpectUnchanged(before)
	}
}

func TestHelpAndVersionOutrankEverythingElseOnTheCommandLine(t *testing.T) {
	// They are the only paths that succeed while skipping the config-load
	// gate, and they take no other action -- not even the force-flag check.
	NewWorld(t).Run("--help", "--force", "--force-no", "backup").
		ExpectExit(0).
		ExpectStdout("Usage:").
		ExpectSilentStderr()

	NewWorld(t).Run("--version", "list").
		ExpectExit(0).
		ExpectStdout("Mackup ").
		ExpectSilentStderr()
}

func TestHelpAndVersionStopTheArgvScanWhereTheyAreFound(t *testing.T) {
	NewWorld(t).Run("--help", "--nope").ExpectExit(0).ExpectStdout("Usage:")
	NewWorld(t).Run("--version", "-z").ExpectExit(0).ExpectStdout("Mackup ")

	// An option that genuinely came first is still rejected.
	NewWorld(t).Run("--nope", "--help").ExpectExit(1).ExpectStderr("--nope")

	// -c takes an argument, so --help here is that argument, not the flag.
	NewWorld(t).Run("-c", "--help", "list").ExpectSilentStdout()
}

func TestUnrecognizedSubcommandWarnsThenPrintsUsageOnStderr(t *testing.T) {
	result := NewWorld(t).Run("frobnicate").
		ExpectExit(1).
		ExpectStderr("frobnicate").
		ExpectStderr("Usage:").
		ExpectSilentStdout()

	if warning, _, _ := strings.Cut(result.Stderr, "\n"); !strings.Contains(warning, "frobnicate") {
		t.Errorf("first stderr line = %q, want the warning ahead of the usage block", warning)
	}
}

func TestABareInvocationShowsUsage(t *testing.T) {
	// appspec/02 "Argument-parser behavior": the parser treats a bare
	// invocation as a usage display, observed exit 0.
	world := NewWorld(t)
	before := world.Snapshot()

	world.Run().ExpectExit(0).ExpectStdout("Usage:")

	world.ExpectUnchanged(before)
}

func TestFormsMatchingNoUsageLineAreUsageErrors(t *testing.T) {
	tests := map[string][]string{
		"unrecognized subcommand":         {"frobnicate"},
		"show without an application":     {"show"},
		"extra positional after list":     {"list", "vim"},
		"extra positional after show":     {"show", "vim", "git"},
		"extra positional after backup":   {"backup", "vim", "git"},
		"extra positional after link":     {"link", "vim", "git"},
		"unknown long option":             {"--nope", "list"},
		"unknown short option":            {"-z", "list"},
		"missing config-file argument":    {"list", "--config-file"},
		"a bare dash is not a key":        {"show", "-"},
		"a bare double dash is not a key": {"--", "list"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			world := NewWorld(t)
			before := world.Snapshot()

			world.Run(args...).
				ExpectExit(1).
				ExpectStderr("Usage:").
				ExpectSilentStdout()

			world.ExpectUnchanged(before)
		})
	}
}

func TestEveryInvocationFormIsAccepted(t *testing.T) {
	// Each listed form must get past the parser. None can succeed yet -- the
	// resolvers and sync operations are later tickets -- so the observable
	// proof that argv matched is that the failure is not a usage error.
	for _, args := range [][]string{
		{"list"},
		{"show", "vim"},
		{"backup"},
		{"backup", "vim"},
		{"restore"},
		{"restore", "vim"},
		{"link"},
		{"link", "vim"},
		{"link", "install"},
		{"link", "install", "vim"},
		{"link", "uninstall"},
		{"link", "uninstall", "vim"},
	} {
		result := NewWorld(t).Run(args...)
		if strings.Contains(result.Stderr, "Usage:") {
			t.Errorf("%s was rejected as a usage error\nstderr: %q", result.invocation(), result.Stderr)
		}
	}
}

func TestOptionsAreAcceptedOnEitherSideOfTheSubcommand(t *testing.T) {
	for _, args := range [][]string{
		{"-f", "-n", "-v", "-r", "backup", "vim"},
		{"backup", "vim", "--force", "--dry-run", "--verbose", "--root"},
		{"--force", "backup", "--dry-run", "vim", "-vr"},
	} {
		result := NewWorld(t).Run(args...)
		if strings.Contains(result.Stderr, "Usage:") {
			t.Errorf("%s was rejected as a usage error\nstderr: %q", result.invocation(), result.Stderr)
		}
	}
}

func TestTheProgramSeesOnlyTheThrowawayHome(t *testing.T) {
	// The rig's whole premise: a case observes the program against a home
	// directory it owns, not the developer's. If this ever fails, every
	// filesystem assertion in this suite is meaningless.
	world := NewWorld(t)
	world.WriteFile(".mackup.cfg", "[storage]\nengine = file_system\n", 0o600)

	before := world.Snapshot()
	if _, ok := before[".mackup.cfg"]; !ok {
		t.Fatalf("the world's home does not contain the file just written to it: %v", before)
	}
	world.Run("--help").ExpectExit(0)
	world.ExpectUnchanged(before)
}
