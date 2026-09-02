//go:build conformance

// The conformance suite is behind a build tag, one of three separate guards
// against a cached "ok" outliving the program it was a result about. The tag
// alone does not close it -- `go test -tags conformance ./...` is the hole it
// leaves, and that is the invocation gopls and GoLand use -- so see the
// header of harness_test.go for which guard covers what before removing any
// of them. Run it with `make conformance` (or
// `go test -count=1 -tags conformance ./test/conformance/`).

package conformance

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
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
	// the program exactly as `make build` produces it -- the artifact a user
	// gets, not one built beside it.
	NewWorld(t).Run("--version").
		ExpectExit(0).
		ExpectStdout("Mackup unknown")
}

func TestVersionReportsTheFallbackTokenForAVCSStampedBuild(t *testing.T) {
	// The harder half of the same rule, and the one that is easy to lose. A
	// build from a working tree is an uninstalled tree, but since Go 1.24 the
	// toolchain labels it with a pseudo-version derived from the commit rather
	// than with "(devel)" -- so a program that trusted the module version
	// would report a build identity here. Whether a build gets stamped at all
	// depends on the machine, which is why this runs against a binary built
	// with the stamp forced on.
	world := NewWorld(t)
	world.UseBinary(requireVCSStampedBuild(t))
	world.Run("--version").
		ExpectExit(0).
		ExpectStdout("Mackup unknown")
}

func TestVersionReportsItsOwnVersionWhenTheBuildCarriesOne(t *testing.T) {
	// A release build, made the way the project makes one: `make build
	// VERSION=...`. That path is otherwise unexercised, and its -X symbol
	// path is spelled out by hand -- the linker accepts a stale one without
	// complaint and simply stamps nothing, so a release binary reporting
	// "unknown" is a silent failure this is the only guard against.
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
	// this is a `list` run with a nonsense config path, not a help request.
	//
	// What is asserted is the parser property alone -- help was not printed --
	// because the stage that rejects this run is going to move. Today nothing
	// reads the config and it reaches dispatch; once loadConfig lands
	// (macklebox-resolvers-5iw.2) "--help" is a config path that does not
	// exist and is not inside the home directory, so the run will abort at the
	// config gate instead. Both end at exit 1 with nothing on stdout, whereas
	// --help taken as the flag would exit 0 with the usage block on stdout,
	// and a crash would not exit 1 at all. Tighten this to the config-error
	// message when that ticket lands.
	result := NewWorld(t).Run("-c", "--help", "list").
		ExpectExit(1).
		ExpectSilentStdout()
	if result.Stderr == "" {
		t.Error("mackup -c --help list said nothing at all; want a diagnostic on stderr")
	}
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
	// appspec/02 "Argument-parser behavior" makes one promise about a bare
	// invocation and withholds two. The promise: "A reimplementation should
	// print the usage block to the user." Withheld: the stream, which it never
	// names here -- only --help/--version are pinned to stdout, and usage
	// *errors* to stderr -- and the exit code, of which it says in as many
	// words that "matching the exact exit code here is not load-bearing for
	// callers".
	//
	// So this asserts the promise and nothing else. Routing this block to
	// stderr, or exiting 1, breaks no contract and must not fail here. That
	// this implementation prints to stdout and exits 0 is its own choice, and
	// is pinned where a choice belongs: internal/app's own
	// TestBareInvocationShowsUsage.
	world := NewWorld(t)
	before := world.Snapshot()

	world.Run().ExpectEitherStream(usageMarker)

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
		"a bare double dash is not a key": {"--", "list"},
	}
	// {"show", "-"} was here and is deliberately gone. appspec/02 never
	// mentions a bare "-": its grammar is `show <application>`, which as a
	// grammar accepts any token, and the spec's own parser-behavior section
	// records reactions to a missing or duplicate positional and nothing
	// about this one. This implementation rejects "-" as an unrecognized
	// argument, so asserting a usage error here pinned the implementation
	// rather than the contract -- and if "-" does bind as an application, the
	// contract is the literal "Unsupported application: -" instead, which is
	// the opposite answer on the same stream. Left unpinned until the ticket
	// that adds application validation settles it against the reference:
	// macklebox-invocation-0y1.
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
	// Half spec, half policy, and the halves are worth keeping apart.
	//
	// appspec/02-invocation.md says only "Options may appear before the
	// subcommand", so the first form below is the contract. Accepting them
	// after it as well is this implementation's own choice -- stated in its
	// help text, which appspec/02 declares human-facing and not contract -- and
	// the remaining forms pin that choice so it cannot be lost by accident,
	// not because the spec requires it.
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

	// Observed on the environment the process was actually launched with, not
	// on one the harness rebuilds to order: what makes the world isolating is
	// a single assignment in RunWithInput, and deleting it hands the program
	// the developer's own environment while leaving a re-derived value
	// perfectly correct.
	environment := map[string]string{}
	for _, entry := range world.Run("--help").ExpectExit(0).Env {
		name, value, _ := strings.Cut(entry, "=")
		environment[name] = value
	}
	if len(environment) == 0 {
		t.Fatal("the program was launched with no environment of its own, so it inherited the developer's")
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

func TestExpectUnchangedReportsEveryShapeOfChange(t *testing.T) {
	// Six cases carry the spec promises no single command states -- --help
	// touches nothing (appspec/02), a rejected run leaves the filesystem
	// alone, --dry-run performs no copy (appspec/01 sections 3 and 6) -- and
	// every one of them makes that assertion through ExpectUnchanged. Nothing
	// checked that ExpectUnchanged reports anything at all: replacing its body
	// with `_ = before` left the whole suite reporting ok, and dropping the
	// content field from Snapshot did too. An assertion that cannot fail is
	// not an assertion, so this pins the comparison directly.
	//
	// A world per shape. Sharing one would let a shape observe the wreckage
	// the previous one left, and the first draft of this case did exactly
	// that -- the removal ran, its restore did not, and the shape after it
	// failed on a missing file rather than on what it was checking.
	const original = "[storage]\nengine = file_system\n"

	t.Run("nothing changed", func(t *testing.T) {
		// First, so the shapes below cannot pass by way of an ExpectUnchanged
		// that reports unconditionally.
		world := NewWorld(t)
		world.WriteFile(".mackup.cfg", original, 0o600)
		if reported := world.captureReport(t, func() { world.ExpectUnchanged(world.Snapshot()) }); len(reported) != 0 {
			t.Errorf("ExpectUnchanged reported %v with nothing changed", reported)
		}
	})

	t.Run("a file created", func(t *testing.T) {
		world := NewWorld(t)
		before := world.Snapshot()
		world.WriteFile("appeared", "", 0o600)
		expectReported(t, world.captureReport(t, func() { world.ExpectUnchanged(before) }),
			world.SnapshotKey("appeared"), "was created")
	})

	t.Run("a file removed", func(t *testing.T) {
		world := NewWorld(t)
		path := world.WriteFile(".mackup.cfg", original, 0o600)
		before := world.Snapshot()
		if err := os.Remove(path); err != nil {
			t.Fatalf("removing %s: %v", path, err)
		}
		expectReported(t, world.captureReport(t, func() { world.ExpectUnchanged(before) }),
			world.SnapshotKey(".mackup.cfg"), "was removed")
	})

	t.Run("content replaced at the same size and the same modification time", func(t *testing.T) {
		// The shape that pins the content field specifically. A rewrite moves
		// the stamp, so a plain edit is caught by the stamp alone and would
		// stay green with content dropped from Snapshot -- the mutation that
		// motivated this case. Restoring the stamp leaves the bytes as the
		// only field that differs. This is the mirror of the case below,
		// which holds the bytes and moves the stamp.
		//
		// Nothing else in the world moves, so this shape expects exactly one
		// report: rewriting a file does not touch its directory's stamp, and
		// creating or removing one does, which is why the shapes above look
		// for their own entry among several.
		world := NewWorld(t)
		path := world.WriteFile(".mackup.cfg", original, 0o600)
		key := world.SnapshotKey(".mackup.cfg")
		stat, err := os.Stat(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		before := world.Snapshot()

		if err := os.WriteFile(path, []byte(strings.Repeat("x", len(original))), 0o600); err != nil {
			t.Fatalf("rewriting %s: %v", path, err)
		}
		if err := os.Chtimes(path, stat.ModTime(), stat.ModTime()); err != nil {
			t.Fatalf("restoring the modification time of %s: %v", path, err)
		}

		// The instrument, checked before it is trusted. If the filesystem
		// would not take the stamp back -- a coarse or truncating one -- the
		// stamp differs too and this case would pass without the content
		// field ever being consulted, which is the one thing it exists to
		// prove.
		restored, err := os.Stat(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if !restored.ModTime().Equal(stat.ModTime()) {
			t.Skipf("this filesystem would not restore %s's modification time (%s, then %s after Chtimes), so a same-size rewrite is visible through the stamp and this case cannot isolate the content field", key, stat.ModTime(), restored.ModTime())
		}

		reported := world.captureReport(t, func() { world.ExpectUnchanged(before) })
		if len(reported) != 1 {
			t.Fatalf("ExpectUnchanged reported %d changes, want exactly 1 -- only the rewritten file moved: %v", len(reported), reported)
		}
		expectReported(t, reported, key, "changed")
	})
}

// expectReported asserts that some report names both the path and the kind of
// change. Creating or removing a file also moves its directory's modification
// time, so a shape legitimately produces more than one report; what must not
// happen is the entry itself going unreported.
func expectReported(t *testing.T, reported []string, path, want string) {
	t.Helper()
	for _, message := range reported {
		if strings.Contains(message, path) && strings.Contains(message, want) {
			return
		}
	}
	t.Errorf("no report names %s as %q; ExpectUnchanged reported %v", path, want, reported)
}

func TestTheSnapshotSeesAFileRewrittenWithTheBytesItAlreadyHeld(t *testing.T) {
	// appspec/01 section 3's dry-run contract is "perform no copy/move/delete/
	// symlink", not "leave the same bytes". A copy that ran when it should not
	// have, onto a destination that already matched, is precisely the
	// regression ExpectUnchanged exists to catch -- and it is invisible to a
	// snapshot that records only content and mode.
	world := NewWorld(t)
	const content = "[storage]\nengine = file_system\n"
	path := world.WriteFile(".mackup.cfg", content, 0o600)

	before := world.Snapshot()
	key := world.SnapshotKey(".mackup.cfg")

	// Modification-time resolution varies by an enormous factor -- nanoseconds
	// on APFS and ext4, one scheduler tick on Linux's coarse inode clock, a
	// full second on ext3 and HFS+ -- so the rewrite is repeated until the
	// stamp actually moves rather than after a fixed sleep chosen to be "long
	// enough", which on a one-second filesystem it would not be.
	// The filesystem is asked directly what it did, so that the two reasons
	// this case might not see a change stay apart. Skipping on "the snapshot
	// did not change" alone would fold them together, and the case could then
	// only pass or skip: dropping the stamp from Snapshot entirely made this
	// skip after 3s and the whole suite report "ok", with every
	// ExpectUnchanged silently blind to the copy it exists to catch.
	original, err := os.Stat(path)
	if err != nil {
		t.Fatalf("reading the modification time of %s: %v", path, err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("rewriting %s: %v", path, err)
		}
		rewritten, err := os.Stat(path)
		if err != nil {
			t.Fatalf("reading the modification time of %s: %v", path, err)
		}
		if !rewritten.ModTime().Equal(original.ModTime()) {
			// The OS moved the stamp, so the snapshot has no excuse.
			if got := world.Snapshot()[key]; got == before[key] {
				t.Errorf("the filesystem moved %s's modification time from %s to %s, but its snapshot entry is unchanged at %q; Snapshot no longer records the stamp, so ExpectUnchanged cannot see a rewrite that preserves the bytes",
					key, original.ModTime(), rewritten.ModTime(), got)
			}
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Said out loud rather than asserted away: where the stamp will not move
	// within the deadline, ExpectUnchanged genuinely cannot see a rewrite that
	// preserves the bytes, and any case relying on that -- appspec/01 section
	// 3's dry-run contract above all -- is weaker on this filesystem. This is
	// now reached only when the OS itself held the stamp still.
	t.Skipf("this filesystem's modification-time resolution is too coarse to register a rewrite of %s within %s, so a same-bytes rewrite is invisible to ExpectUnchanged here", key, 3*time.Second)
}
