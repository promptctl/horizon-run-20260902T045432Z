package conformance

import (
	"os"
	"path/filepath"
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

		// appspec/02 makes two things contract here and no more: --help goes
		// to stdout and exits 0. The wording of the block itself is
		// "human-facing and is not contract", so nothing below pins a line of
		// it -- a case that pinned help text would fail on a rewording that
		// broke no promise, and would still pass if the grammar changed.
		world.Run(flag).
			ExpectExit(0).
			ExpectStdout(usageMarker).
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
		ExpectStdout(usageMarker).
		ExpectSilentStderr()

	NewWorld(t).Run("--version", "list").
		ExpectExit(0).
		ExpectStdout("Mackup ").
		ExpectSilentStderr()
}

func TestHelpAndVersionStopTheArgvScanWhereTheyAreFound(t *testing.T) {
	NewWorld(t).Run("--help", "--nope").ExpectExit(0).ExpectStdout(usageMarker)
	NewWorld(t).Run("--version", "-z").ExpectExit(0).ExpectStdout("Mackup ")

	// An option that genuinely came first is still rejected.
	NewWorld(t).Run("--nope", "--help").ExpectExit(1).ExpectStderr("--nope")

	// -c takes an argument, so --help here is that argument, not the flag:
	// this is a `list` run with a (nonsense) config path, not a help request.
	// Silence on stdout alone would not show that -- a crashed binary is
	// silent too -- so the case asserts the run reached list.
	NewWorld(t).Run("-c", "--help", "list").ExpectNotImplemented("list")
}

func TestUnrecognizedSubcommandWarnsThenPrintsUsageOnStderr(t *testing.T) {
	result := NewWorld(t).Run("frobnicate").
		ExpectExit(1).
		ExpectStderr("frobnicate").
		ExpectStderr(usageMarker).
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

	world.Run().ExpectExit(0).ExpectStdout(usageMarker)

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
				ExpectStderr(usageMarker).
				ExpectSilentStdout()

			world.ExpectUnchanged(before)
		})
	}
}

func TestEveryInvocationFormIsAcceptedAndReachesItsCommand(t *testing.T) {
	// Every usage line of appspec/02, with the command each form selects.
	// None can succeed yet -- the resolvers and sync operations are later
	// tickets -- so what is observable is that the form got past the parser
	// and reached that command's dispatch arm. Asserting the arm, rather than
	// the absence of a usage error, is what makes this case able to fail:
	// "no usage error" also holds when the program is broken in some other
	// way, or prints its usage block under a different first word.
	for _, test := range []struct {
		args []string
		cmd  string
	}{
		{[]string{"list"}, "list"},
		{[]string{"show", "vim"}, "show"},
		{[]string{"backup"}, "backup"},
		{[]string{"backup", "vim"}, "backup"},
		{[]string{"restore"}, "restore"},
		{[]string{"restore", "vim"}, "restore"},
		{[]string{"link"}, "link"},
		{[]string{"link", "vim"}, "link"},
		{[]string{"link", "install"}, "link install"},
		{[]string{"link", "install", "vim"}, "link install"},
		{[]string{"link", "uninstall"}, "link uninstall"},
		{[]string{"link", "uninstall", "vim"}, "link uninstall"},
	} {
		NewWorld(t).Run(test.args...).ExpectNotImplemented(test.cmd)
	}
}

func TestOptionsAreAcceptedOnEitherSideOfTheSubcommand(t *testing.T) {
	// appspec/02: "Options may be given before or after the subcommand." Each
	// form must select the same command whichever side its options sit on.
	for _, args := range [][]string{
		{"-f", "-n", "-v", "-r", "backup", "vim"},
		{"backup", "vim", "--force", "--dry-run", "--verbose", "--root"},
		{"--force", "backup", "--dry-run", "vim", "-vr"},
	} {
		NewWorld(t).Run(args...).ExpectNotImplemented("backup")
	}
}

func TestTheHarnessIsolatesTheProgramFromTheDeveloperEnvironment(t *testing.T) {
	// The rig's whole premise: a case observes the program against a home
	// directory and an environment it owns, not the developer's. If this ever
	// fails, every filesystem assertion in this suite is meaningless.
	//
	// This asserts the isolation itself, not a consequence of it. No command
	// implemented so far reads HOME -- appspec/02 says --help reads no config
	// at all -- so a case that ran a command and watched nothing happen would
	// pass identically with the developer's home leaked in, and could not fail
	// for the reason it claimed. Add the program-side observation once a
	// HOME-reading command lands (macklebox-resolvers-5iw.2).
	for _, name := range []string{"XDG_CONFIG_HOME", "MACKUP_CONFIG"} {
		t.Setenv(name, "/developer-machine/must-not-leak")
	}

	world := NewWorld(t)

	realHome := os.Getenv("HOME")
	if realHome == "" {
		t.Fatal("the developer environment has no HOME, so this case cannot check that the world's home differs from it")
	}
	if world.Home == realHome {
		t.Errorf("the world's home is the real HOME %q", realHome)
	}

	environment := map[string]string{}
	for _, entry := range world.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		environment[name] = value
	}
	if environment["HOME"] != world.Home {
		t.Errorf("HOME in the program's environment = %q, want the world's home %q", environment["HOME"], world.Home)
	}
	for _, name := range []string{"XDG_CONFIG_HOME", "MACKUP_CONFIG"} {
		if value, ok := environment[name]; ok {
			t.Errorf("%s leaked from the developer environment into the program's, as %q", name, value)
		}
	}
}

func TestTheSnapshotWatchesTheWholeScratchRoot(t *testing.T) {
	// A "changed nothing" assertion is only as wide as what the snapshot
	// walks. appspec/04's file_system engine takes an arbitrary path, so the
	// Mackup folder need not live under home, and the command runs with its
	// working directory at the scratch root -- a snapshot of home alone would
	// be blind to exactly the storage-side and cwd writes these assertions
	// exist to catch.
	world := NewWorld(t)
	world.WriteFile(".mackup.cfg", "[storage]\nengine = file_system\n", 0o600)

	before := world.Snapshot()
	if _, ok := before[world.SnapshotKey(".mackup.cfg")]; !ok {
		t.Fatalf("the snapshot does not contain the file just written to home: %v", before)
	}

	outside := filepath.Join("storage", "Mackup")
	if err := os.MkdirAll(filepath.Join(world.Root, outside), 0o700); err != nil {
		t.Fatalf("creating %s: %v", outside, err)
	}
	if _, ok := world.Snapshot()[outside]; !ok {
		t.Errorf("the snapshot is blind to %s, created outside home", outside)
	}
}
