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
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
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

	world.Run("--version").ExpectExit(0).ExpectSilentStderr().ExpectVersionLine()
	world.ExpectUnchanged(before)
}

func TestVersionReportsTheFallbackTokenForAnUninstalledBuild(t *testing.T) {
	// appspec/00-overview.md "Provenance": the version is the package's own
	// version when installed, and a stable fallback token otherwise. This is
	// the program exactly as `make build` produces it -- the artifact a user
	// gets, not one built beside it.
	NewWorld(t).Run("--version").
		ExpectExit(0).
		ExpectStdoutLine("Mackup unknown")
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
		ExpectStdoutLine("Mackup unknown")
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
		ExpectStdoutLine("Mackup " + stampedVersion)
}

func TestAReleaseBuildOutranksTheProvenanceOfItsCheckout(t *testing.T) {
	// The precedence half of appspec/00 "Provenance", which no case reached.
	//
	// internal/version resolves the linker stamp before it looks at build
	// info, so a release binary reports its own version even though it was
	// built from a checkout and carries that checkout's vcs.revision. Every
	// other version case observes a binary where only one of the two sources
	// is present, and the release binary the suite already had is built with
	// -buildvcs=auto -- unstamped wherever the toolchain declines, which
	// includes any machine with GOFLAGS=-buildvcs=false. With no vcs.revision
	// there is no conflict, so inverting the precedence in internal/version
	// left this whole suite green and only the unit test failed. Observed.
	//
	// This binary carries both at once, which is the only state where the rule
	// decides anything.
	world := NewWorld(t)
	world.UseBinary(requireStampedVCSBuild(t))
	world.Run("--version").
		ExpectExit(0).
		ExpectStdoutLine("Mackup " + stampedVersion).
		ExpectSilentStderr()
}

func TestARefreshedBuildDirectoryOutlivesTheReaper(t *testing.T) {
	// The other half of the reaper contract. A running suite refreshes its own
	// build directory's modification time, because the mtime is otherwise set
	// once -- when the binaries are built -- so "untouched for an hour" would
	// mean "started over an hour ago", and the next run to start would delete
	// a long run's binaries out from under it. That surfaces as an exec
	// failure in whatever case happened to be running, which names neither
	// cause nor culprit.
	//
	// Nothing pinned it: replacing the Chtimes call with `_ = now` left the
	// whole suite green. The reaper's other half, that it judges the entry it
	// removes, has had a case since it was written.
	//
	// Both directions are asserted in one case on purpose. A reaper that
	// simply never reaps also leaves a refreshed directory standing, so the
	// stale one is what makes the survival mean something.
	within := t.TempDir()
	stale := filepath.Join(within, buildDirPrefix+"stale")
	refreshed := filepath.Join(within, buildDirPrefix+"refreshed")
	for _, dir := range []string{stale, refreshed} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
		// Well past the threshold, so both are reapable before the refresh.
		aged := time.Now().Add(-2 * buildDirAbandonedAfter)
		if err := os.Chtimes(dir, aged, aged); err != nil {
			t.Fatalf("ageing %s: %v", dir, err)
		}
	}

	touchBuildDir(refreshed)
	reapAbandonedBuildDirectories(within)

	if _, err := os.Stat(refreshed); err != nil {
		t.Errorf("the refreshed build directory was reaped (%v); a suite running longer than %s would have had its binaries deleted mid-run", err, buildDirAbandonedAfter)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("the stale build directory survived, so this case cannot show that the refresh is what saved the other one")
	}
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
		ExpectVersionLine().
		ExpectSilentStderr()
}

func TestHelpAndVersionStopTheArgvScanWhereTheyAreFound(t *testing.T) {
	// Both assert a silent stderr, which is the half that says the scan
	// stopped. Without it a parser that walks all of argv, warns about the
	// later token on stderr and then honors the --help or --version it also
	// saw passes both -- verified by emitting "mackup: unrecognized option:
	// --nope" on this path and watching the suite stay green. The sibling case
	// above asserts it on the same shape of argv; these two did not.
	NewWorld(t).Run("--help", "--nope").
		ExpectExit(0).
		ExpectStdout(usageMarker).
		ExpectSilentStderr()
	NewWorld(t).Run("--version", "-z").
		ExpectExit(0).
		ExpectVersionLine().
		ExpectSilentStderr()

	// An option that genuinely came first is still rejected, and the --help
	// behind it is not honored -- which is what the silent stdout says, since
	// honoring it would have put the usage block there.
	//
	// Non-zero rather than 1, for the reason the usage-error table gives at
	// length: the code a parser answers with is not contract, and that table's
	// {"--nope", "list"} is this same rejection, left unpinned. Pinning it
	// here failed a reimplementation answering the conventional 2 that the
	// table deliberately passes.
	NewWorld(t).Run("--nope", "--help").
		ExpectFailureExit().
		ExpectStderr("--nope").
		ExpectSilentStdout()

	// -c takes an argument, so --help here is that argument, not the flag:
	// this is a `list` run with a nonsense config path, not a help request.
	//
	// Asserted the way every other "this form was accepted" case is: the run
	// reached its dispatch arm, positively. The earlier spelling asked for
	// exit 1 and a non-empty stderr, and the 1 it matched came from the
	// not-implemented stub -- scaffolding of exactly the kind
	// ExpectNotImplemented exists to quarantine, read as though it were the
	// spec's exit code for this form. It also could not fail for the reason it
	// claimed: any breakage exiting 1 with something on stderr satisfied it,
	// including ones where --help was never taken as the -c argument at all.
	//
	// The stage that ends this run is going to move, and this case is meant to
	// fail when it does. Once loadConfig lands (macklebox-resolvers-5iw.2)
	// "--help" is a config path that neither exists nor sits inside the home
	// directory, so the run aborts at the config gate and never reaches
	// dispatch. Replace this with the config-error message then, the same way
	// every other ExpectNotImplemented is replaced as its command lands.
	NewWorld(t).Run("-c", "--help", "list").ExpectNotImplemented("list")
}

func TestUnrecognizedSubcommandWarnsThenPrintsUsageOnStderr(t *testing.T) {
	// Non-zero rather than 1, for the reason
	// TestFormsMatchingNoUsageLineAreUsageErrors spells out over this very
	// argv: it runs {"frobnicate"} too and asserts only that the run failed.
	// appspec/07's error table does not list usage errors among the conditions
	// it gives exit 1, and appspec/02 records no exit code for this form at
	// all. (It says "matching the exact exit code here is not load-bearing for
	// callers" of the ONE parser code it does record, the bare invocation's --
	// citing that line as a general disclaimer, as this comment first did,
	// overstates it. The conclusion rests on appspec/07.) So a
	// reimplementation answering the conventional 2 passed there and failed
	// here, on identical input -- the rule was applied to the table and not to
	// the case standing beside it. That this implementation answers 1 is
	// pinned in internal/app's case of the same name.
	//
	// What is contract here, and is why this case exists apart from the table:
	// appspec/02 says an unrecognized positional "prints a warning line
	// identifying the unmatched argument, then the usage block", and appspec/07
	// puts both on stderr. The order is asserted below.
	result := NewWorld(t).Run("frobnicate").
		ExpectFailureExit().
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
		"unrecognized subcommand":       {"frobnicate"},
		"show without an application":   {"show"},
		"extra positional after list":   {"list", "vim"},
		"extra positional after show":   {"show", "vim", "git"},
		"extra positional after backup": {"backup", "vim", "git"},
		"extra positional after link":   {"link", "vim", "git"},
		"unknown long option":           {"--nope", "list"},
		"unknown short option":          {"-z", "list"},
		"missing config-file argument":  {"list", "--config-file"},
	}
	// The exit code is deliberately not pinned, and the streams deliberately
	// are. appspec/07 says in as many words that stderr carries "argument-
	// parser usage and warning text on a usage error", so the stream and the
	// silent stdout are contract. The number is not: appspec/07's error table
	// gives exit 1 to an enumerated list of fatal conditions and a usage error
	// is not among them, and appspec/02 says of the one parser exit code it
	// does record that "matching the exact exit code here is not load-bearing
	// for callers". A reimplementation using the conventional 2 -- which is
	// what the reference's own argparse does -- breaks nothing and must not
	// fail here.
	//
	// Non-zero is still asserted, and is not a token gesture: appspec/07 has
	// a supervisor "detecting failure" read the exit code, and exit 0 would
	// make these forms indistinguishable from the successful run appspec/02's
	// table defines 0 as. That this implementation answers 1 is its own
	// choice, pinned where a choice belongs -- internal/app's
	// TestUnrecognizedSubcommandWarnsThenPrintsUsageOnStderr and
	// TestShowWithoutApplicationIsAUsageError, both of which assert
	// ExitFailure. This is the same line TestABareInvocationShowsUsage draws;
	// this table was on the wrong side of it.
	//
	// {"show", "-"} and {"--", "list"} were both here and are deliberately
	// gone, for one reason: appspec/02 settles neither, so a case here fails
	// a conformant reimplementation for making a different defensible choice.
	//
	// The spec's grammar is `show <application>`, which as a grammar accepts
	// any token, and its parser-behavior section enumerates reactions to a
	// missing or duplicate positional and to an unrecognized subcommand --
	// nothing about either dash. What the two conventional parsers do with
	// them was run rather than recalled, and they disagree with each other on
	// both forms: argparse reads `-- list` as an end-of-options marker and
	// runs list, exiting 0, while rejecting a bare "-"; docopt does the
	// reverse, rejecting `-- list` -- it strips the "--", and the token left
	// over is then an argument where the pattern wants the command -- and
	// binding `show -` to <application> "-". Two reasonable parsers, opposite
	// answers on each form, and a spec that names neither: the suite must not
	// pick a side the spec declines to take. In particular, do not restore
	// either case on the argument that the reference "would" accept it; that
	// premise was checked and does not hold in one direction or the other.
	//
	// Neither form is left unasserted, and neither is a claim that this
	// implementation is wrong -- it rejects both, agreeing with docopt on
	// "--" and with argparse on "-". That is its own choice, pinned where a
	// choice belongs: internal/cli's TestParseRejectsABareDash and
	// TestParseRejectsTheUndocumentedDoubleDash. If "-" does bind as an
	// application the contract becomes the literal "Unsupported application:
	// -" instead, the opposite answer on the same stream.
	// macklebox-invocation-0y1 settles both against the reference.
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			world := NewWorld(t)
			before := world.Snapshot()

			world.Run(args...).
				ExpectFailureExit().
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

	// TMPDIR has to land inside the root or the snapshot is not watching the
	// whole world: a temporary file written through os.TempDir -- an atomic
	// copy is the ordinary way to implement appspec/01 section 3 -- would go
	// to the system temp directory, outside everything ExpectUnchanged walks,
	// and "changed nothing" would hold over a run that wrote.
	tmp, ok := environment["TMPDIR"]
	if !ok {
		t.Error("the program's environment has no TMPDIR, so anything it writes through os.TempDir lands outside the scratch root and outside every filesystem assertion in this suite")
	} else if relative, err := filepath.Rel(world.Root, tmp); err != nil || strings.HasPrefix(relative, "..") {
		t.Errorf("TMPDIR in the program's environment = %q, which is outside the scratch root %q", tmp, world.Root)
	}
}

func TestAVariableTheWorldSetsReachesTheProgram(t *testing.T) {
	// World.Setenv is how a case points the program at a config file or an XDG
	// directory of its own (macklebox-resolvers-5iw.2 is the first that will).
	// Nothing called it, so nothing checked that what it sets actually
	// arrives, and the isolation case next door cannot: that one asserts
	// variables are ABSENT from the program's environment, which a Setenv
	// dropping its value on the floor satisfies perfectly.
	//
	// Observed on the environment the process was launched with, for the
	// reason that case gives: a value re-derived from the world would read
	// correctly even if nothing reached the program.
	world := NewWorld(t)
	want := world.Path("elsewhere.cfg")
	world.Setenv("MACKUP_CONFIG", want)

	environment := map[string]string{}
	for _, entry := range world.Run("--help").ExpectExit(0).Env {
		name, value, _ := strings.Cut(entry, "=")
		environment[name] = value
	}
	if got := environment["MACKUP_CONFIG"]; got != want {
		t.Errorf("MACKUP_CONFIG in the program's environment = %q, want %q", got, want)
	}
}

func TestTheSnapshotRecordsASymlinkByItsTargetWithoutFollowingIt(t *testing.T) {
	// A harness case, like the FIFO one in harness_unix_test.go: it pins a
	// field of the snapshot record that every ExpectUnchanged in the suite
	// rests on.
	//
	// The target IS the content of a symlink. Nothing else in the record
	// separates a link pointing at one file from a link pointing at another --
	// the permission bits are identical and no size is recorded -- so a
	// snapshot that dropped it reports "unchanged" over a re-pointed link.
	// appspec/05's link engine replaces a dotfile with a symlink into the
	// Mackup folder, and both the dry-run and the rejected-run post-conditions
	// there are "no filesystem change", asserted with ExpectUnchanged.
	//
	// Pinned by reading the field rather than by re-pointing the link, and
	// that is the whole point of the case. Re-pointing means removing and
	// recreating, which moves the link's own mtime, so the mtime field alone
	// would report the change and the target would stay unproven -- the
	// vacuous-pass shape this suite keeps finding one level over from where it
	// last looked. Reading the field is what makes the battery's "the symlink
	// target is not recorded" entry fail; before this case existed, replacing
	// os.Readlink with a constant left the whole gate green.
	world := NewWorld(t)
	world.WriteFile("real.txt", "the target's own bytes", 0o600)
	link := world.Path("link")
	if err := os.Symlink("real.txt", link); err != nil {
		t.Fatalf("creating a symlink at %s: %v", link, err)
	}

	snapshot := world.Snapshot()
	key := world.SnapshotKey("link")
	got, ok := snapshot[key]
	if !ok {
		t.Fatalf("snapshot has no entry for %s; it holds %v", key, snapshotPaths(snapshot))
	}
	if !strings.HasPrefix(got, "symlink ") {
		t.Errorf("snapshot recorded %s as %q, want it to begin with %q", key, got, "symlink ")
	}
	if want := "-> real.txt"; !strings.HasSuffix(got, want) {
		t.Errorf("snapshot recorded %s as %q, want it to end with %q; without the target a re-pointed link is indistinguishable from an untouched one", key, got, want)
	}
	// Lstat, not Stat: a snapshot that followed the link would record the
	// target's bytes here and then report a phantom change on every run that
	// touched the target rather than the link.
	if strings.Contains(got, "the target's own bytes") {
		t.Errorf("snapshot recorded %s as %q; it followed the link and read the target rather than recording the link itself", key, got)
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

func TestTheReaperJudgesTheEntryItRemoves(t *testing.T) {
	// The reaper decides from a stat and acts with RemoveAll, and RemoveAll
	// unlinks a symlink rather than following it -- so the stat has to be an
	// Lstat, or the two disagree about which object is under discussion. Under
	// an os.Stat a symlink created seconds ago is destroyed because the
	// directory it points at is old, which is the reaper deleting something
	// that is not abandoned by any reading.
	//
	// This is the direction of the defect that can be observed. The other --
	// a long-stale link that an os.Stat never reaps, because its live target
	// keeps reporting a fresh stamp -- needs a symlink whose own modification
	// time has been moved back, and nothing in the standard library can do
	// that: os.Chtimes follows the link and rewrites the target's stamp
	// instead, or fails outright when there is no target. Written that way
	// first, this case could only pass or skip, so it asserts the half it can
	// actually make fail.
	//
	// Swept in a scratch directory rather than the real TMPDIR, which a case
	// has no business emptying: another conformance run on the same machine
	// keeps its build directory there.
	within := t.TempDir()
	stale := time.Now().Add(-2 * buildDirAbandonedAfter)

	// Outside the swept directory, so the sweep reaches it only through the
	// link and the case cannot pass by way of the target being reaped too.
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("creating %s: %v", target, err)
	}
	if err := os.Chtimes(target, stale, stale); err != nil {
		t.Fatalf("ageing %s: %v", target, err)
	}

	link := filepath.Join(within, buildDirPrefix+"link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("this filesystem does not support symlinks: %v", err)
	}

	fresh := filepath.Join(within, buildDirPrefix+"fresh")
	abandoned := filepath.Join(within, buildDirPrefix+"abandoned")
	for _, dir := range []string{fresh, abandoned} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
	}
	if err := os.Chtimes(abandoned, stale, stale); err != nil {
		t.Fatalf("ageing %s: %v", abandoned, err)
	}

	reapAbandonedBuildDirectories(within)

	// The reason the case exists: the link's own stamp is seconds old.
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("the sweep removed %s, which was created moments ago; it was judged by the modification time of what it points at rather than its own", filepath.Base(link))
	}
	// There was an assertion here that the link's target survived. It could
	// not fail, and it is gone rather than reworded: RemoveAll's first act is
	// Remove, which unlinks a symlink instead of descending it, so the target
	// survives under the Stat mutation too -- verified, along with a file
	// inside it. No mutation of this reaper reaches the target, so nothing
	// here can witness one that did.
	// And the ordinary behaviour still holds, so the case above cannot be
	// satisfied by a reaper that reaps nothing at all.
	if _, err := os.Lstat(abandoned); !os.IsNotExist(err) {
		t.Errorf("%s went untouched for twice the abandonment window and survived the sweep (lstat error: %v)", filepath.Base(abandoned), err)
	}
	if _, err := os.Lstat(fresh); err != nil {
		t.Errorf("%s was created moments ago and should have survived: %v", filepath.Base(fresh), err)
	}
}

func TestTheReaperFindsDirectoriesUnderAPathHoldingAGlobMetacharacter(t *testing.T) {
	// The sweep used filepath.Glob, which reads the directory's own path as
	// pattern text rather than as a literal path. A TMPDIR holding "[", "*"
	// or "?" -- a developer's, or a runner's -- then reclaimed nothing, for
	// good, with no signal: every error in the sweep is swallowed by design.
	within := filepath.Join(t.TempDir(), "tmp[1]")
	if err := os.Mkdir(within, 0o700); err != nil {
		t.Fatalf("creating %s: %v", within, err)
	}

	abandoned := filepath.Join(within, buildDirPrefix+"abandoned")
	if err := os.Mkdir(abandoned, 0o700); err != nil {
		t.Fatalf("creating %s: %v", abandoned, err)
	}
	stale := time.Now().Add(-2 * buildDirAbandonedAfter)
	if err := os.Chtimes(abandoned, stale, stale); err != nil {
		t.Fatalf("ageing %s: %v", abandoned, err)
	}

	// Not named with the prefix, so it must survive: this is what keeps the
	// case honest about a sweep that simply removed everything it found.
	bystander := filepath.Join(within, "not-ours")
	if err := os.Mkdir(bystander, 0o700); err != nil {
		t.Fatalf("creating %s: %v", bystander, err)
	}
	if err := os.Chtimes(bystander, stale, stale); err != nil {
		t.Fatalf("ageing %s: %v", bystander, err)
	}

	reapAbandonedBuildDirectories(within)

	if _, err := os.Lstat(abandoned); !os.IsNotExist(err) {
		t.Errorf("%s is stale and named with the suite's prefix but survived a sweep of %s (lstat error: %v)", filepath.Base(abandoned), filepath.Base(within), err)
	}
	if _, err := os.Lstat(bystander); err != nil {
		t.Errorf("%s does not carry the suite's prefix and should not have been touched: %v", filepath.Base(bystander), err)
	}
}

func TestAssertionsOnAResultSurviveACapturedRegion(t *testing.T) {
	// captureReport swaps the world's reporter for the duration of a call.
	// A Result that had copied the reporter when the process ran would keep
	// the recorder after the capture ended, and every assertion made on it
	// afterwards would report into an object nobody reads -- green whatever
	// the program did. No case is written that way today; this is what stops
	// the next one, since the failure leaves no trace at all.
	world := NewWorld(t)

	var got Result
	if reported := world.captureReport(t, func() { got = world.Run("--help") }); len(reported) != 0 {
		t.Fatalf("running --help inside a capture reported %v; this case needs a clean run to assert on", reported)
	}

	// A deliberately wrong expectation, outside the capture that produced the
	// Result. It has to be heard.
	reported := world.captureReport(t, func() { got.ExpectExit(99) })
	if len(reported) == 0 {
		t.Error("an assertion on a Result produced inside a captured region reported nothing; the Result is still reporting into the recorder that region used, so every assertion made on it is silently discarded")
	}
}

func TestEveryDocCommentNamesWhatItDocuments(t *testing.T) {
	// Go's convention -- a doc comment opens with the name it documents -- is
	// worth enforcing here for a specific reason rather than as style. Three
	// times on this branch a declaration was slid between a doc comment and
	// the thing it described, silently reassigning the comment: the reporter
	// interface took World's, ExpectFailureExit took ExpectStdout's, and a
	// var block took the whole explanation of readImplementationSources, so
	// godoc attributed the cache-key mechanism to two path constants. Nothing
	// failed any of the three times.
	//
	// The first version of this case caught two of them and was reported as
	// having found every live violation. It had not: it looked only at
	// functions and single-specification type declarations, so the var block
	// -- the third instance, already in the tree when the case was written --
	// was invisible to it. Both halves below exist because of that.
	root, err := moduleRoot()
	if err != nil {
		t.Fatalf("locating the module root: %v", err)
	}

	// The whole module, not a list of directories the program is believed to
	// live in. The list was cmd, internal and test, and a RENAME of one of
	// those failed loudly (WalkDir errors into the Fatalf below) while an
	// ADDITION was silent: a new top-level package escaped this guard
	// entirely, and the checked == 0 backstop could not fire because the
	// three surviving directories still held documented declarations. That is
	// the same narrowing readImplementationSources refuses forty lines away in
	// harness_test.go -- "never narrow the walk back to a guess at where the
	// program lives" -- and it was being made here at the same time.
	//
	// The skips mirror that walk's, for its reasons: .git is history at
	// whatever depth it appears, and bin is skipped only at the module root
	// because that is the one the Makefile writes.
	checked := 0
	{
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// testdata is skipped for the reason `go build` skips it: nothing
			// under it is part of the build, so it is where a package grows
			// fixtures that are deliberately not valid Go -- an ordinary thing
			// for internal/cli, which is a parser, to want. Parsing one here
			// fatals this case with a message about doc comments, naming a
			// file no doc convention applies to.
			//
			// gofmt is the OTHER half of that, and skipping it only here would
			// have fixed nothing: `gofmt -l ./cmd ./internal ./test` exits 2
			// on the same file and the check target turns a non-zero gofmt
			// into a failed gate, so the red would have moved from this case
			// to that one. The Makefile excludes testdata too. Both are
			// needed; neither is sufficient. Verified by putting the fixture
			// in and running each half with the other half's fix reverted.
			//
			// What this does NOT skip, deliberately: vendor, and directories
			// whose names begin with "_" or "." other than .git, all of which
			// `go build` also ignores. None exists in this module, and a tree
			// that grows one gets a loud failure here rather than a silent
			// gap -- the direction this suite errs in everywhere else.
			if entry.IsDir() {
				if entry.Name() == "testdata" || entry.Name() == ".git" || path == filepath.Join(root, "bin") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
			if err != nil {
				return fmt.Errorf("parsing %s: %v", path, err)
			}
			relative, _ := filepath.Rel(root, path)
			declared := declaredNames(file)

			for _, declaration := range file.Decls {
				for _, documented := range documentedDeclarations(declaration) {
					names, doc := documented.names, documented.doc
					if doc == nil || len(names) == 0 {
						continue
					}
					words := strings.Fields(doc.Text())
					if len(words) == 0 {
						continue
					}
					checked++
					opening := openingName(words)

					// One name, written on its own: the convention applies in
					// full, and the tree follows it everywhere.
					if !documented.collective {
						if opening == names[0] {
							continue
						}
						// A test entry point is held to the weaker rule the
						// grouped branch below uses: it may open with
						// anything that is not the name of another
						// declaration in this file.
						//
						// Not a style concession. "// Issue 4104." above a
						// TestXxx is how Go itself writes them: of the 1657
						// test functions in go1.25.7's own standard library
						// that carry a leading comment, 1193 do not open with
						// the function's name. Counted with this same parser,
						// not recalled. Holding those to the full convention
						// reddens the gate on a contributor writing ordinary
						// Go -- the same defect leadingArticles exists for,
						// fixed the same way.
						//
						// What is deliberately NOT done, having been
						// proposed: exempting test functions outright, or
						// skipping _test.go files. Either blinds this case to
						// the three defects that caused it to be written --
						// all three were in harness_test.go, and an inserted
						// `func TestInserted(t *testing.T) {}` strands a doc
						// comment exactly as the inserted func did. Under the
						// rule below that insertion is still caught, because
						// the stranded comment opens with a name this file
						// declares. The battery pins both directions.
						//
						// The gap this leaves, stated rather than glossed: a
						// comment stranded on a test function that opens with
						// a word the file does not declare reads as an
						// ordinary non-conforming comment and passes. That is
						// the limit the grouped branch already has -- declared
						// is per-file -- and it is the silent half of a trade
						// whose loud half was rejecting idiomatic Go on every
						// contributor who wrote a test.
						if isTestEntryPoint(relative, declaration) {
							if owner, ok := declared[opening]; ok {
								t.Errorf("%s: the doc comment on the test %s opens with %q, which is the name of the %s declared elsewhere in this file; a declaration was inserted between that comment and what it documents", relative, names[0], opening, owner)
							}
							continue
						}
						t.Errorf("%s: the doc comment on %s opens with %q; either it belongs to something else and a declaration was inserted under it, or it does not follow the convention the rest of the tree does", relative, names[0], opening)
						continue
					}

					// A grouped block is allowed a collective comment that opens
					// with no name at all -- "Exit codes, per the exit-code table
					// of appspec/02" over ExitOK and ExitFailure is right, and
					// demanding a name there would be noise. What it may not do
					// is open with the name of something declared elsewhere in
					// the file, which is what a comment that has lost its
					// declaration looks like.
					//
					// "Grouped" is decided structurally -- a parenthesized
					// block, or one specification introducing several names --
					// and not by counting the names that came out. Counting
					// made the exemption above silently not apply to a
					// `var (...)` block declaring exactly one name, which is
					// the opposite of what this comment promises. Not
					// reachable in the tree today; enumerated here because
					// this guard has had three blind spots and glossing its
					// edges is how they got in.
					if slices.Contains(names, opening) {
						continue
					}
					if owner, ok := declared[opening]; ok {
						t.Errorf("%s: the doc comment on the block declaring %v opens with %q, which is the name of the %s declared elsewhere in this file; a declaration was inserted between that comment and what it documents", relative, names, opening, owner)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking the module root: %v", err)
		}
	}

	// The walk has to have found something, or this passes by reaching no
	// files at all -- a renamed directory, a build tag, a bad root.
	if checked == 0 {
		t.Fatal("no documented declarations were examined, so this case proved nothing")
	}
}

// declaredNames maps every name a file declares -- package-scope declarations
// and methods alike -- to the kind of thing it is, so a doc comment opening
// with one of them can be recognised as belonging to that declaration rather
// than to the one it sits on.
//
// Methods are in deliberately, though a method name is not a package-scope
// name. One of the three orphaned comments that motivated this case was a
// method's (ExpectStdout's, taken by ExpectFailureExit), and a method's doc
// can be slid onto a grouped block exactly as a function's was. Excluding
// methods would make that instance invisible here, which is the shape this
// case has already been caught in once. The cost is the other direction: a
// collective comment on a grouped block that happens to open with a method
// name is reported as orphaned when it is not. That fails loudly, names the
// collision, and is fixed by rewording -- the failure direction worth having.
//
// A method never displaces a package-scope declaration of the same name, so
// the kind named in the diagnostic is the one a reader will actually find at
// package scope. harness_test.go declares both `type Snapshot` and `func (w
// *World) Snapshot`, and calling that one a function sent the reader looking
// for something that is not there.
func declaredNames(file *ast.File) map[string]string {
	names := map[string]string{}
	methods := map[string]string{}
	for _, declaration := range file.Decls {
		switch d := declaration.(type) {
		case *ast.FuncDecl:
			if d.Recv != nil {
				methods[d.Name.Name] = "method"
				continue
			}
			names[d.Name.Name] = "function"
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch sp := spec.(type) {
				case *ast.TypeSpec:
					names[sp.Name.Name] = "type"
				case *ast.ValueSpec:
					for _, name := range sp.Names {
						names[name.Name] = d.Tok.String()
					}
				}
			}
		}
	}
	for name, kind := range methods {
		if _, taken := names[name]; !taken {
			names[name] = kind
		}
	}
	return names
}

// documented pairs a doc comment with the names it is attached to, and says
// whether those names were written as a group -- a parenthesized block, or one
// specification introducing several names -- since a group is allowed a
// collective comment and a lone declaration is not.
type documented struct {
	names      []string
	doc        *ast.CommentGroup
	collective bool
}

// leadingArticles are the words a doc comment may open with before the name.
//
// "// A World is one throwaway environment" is idiomatic Go -- the standard
// library writes doc comments this way constantly ("An Encoder writes JSON
// values to an output stream") -- and demanding the bare name rejected it,
// turning the gate red on a contributor for following the convention
// correctly. Verified, by rewording World's own comment that way and watching
// make check fail.
//
// The article is skipped in the grouped branch too, not only the single-name
// one, and it cuts both ways there: it catches "// A World is ..." orphaned
// onto a block, and it can misread a collective comment that happens to begin
// "The World and ..." as that orphan. The false positive fails loudly and is
// fixed by rewording; the false negative is silent. That is the trade this
// suite makes everywhere else, so it is made the same way here.
//
// Trailing punctuation is deliberately NOT accommodated: "// String() returns"
// is not the convention, and a red gate on it is the right answer.
var leadingArticles = map[string]bool{"A": true, "An": true, "The": true}

// isTestEntryPoint reports whether a declaration is a test function the go
// tool will run: TestXxx, BenchmarkXxx, FuzzXxx or ExampleXxx, at package
// scope, in a _test.go file.
//
// The Xxx rule is the go tool's own -- the character after the prefix must not
// be a lower-case letter -- so `Testing` and `Benchmarking` are ordinary
// functions and stay under the full convention. A method is never one of
// these, whatever it is named, so the receiver is checked rather than assumed
// absent.
func isTestEntryPoint(relative string, declaration ast.Decl) bool {
	function, ok := declaration.(*ast.FuncDecl)
	if !ok || function.Recv != nil || !strings.HasSuffix(relative, "_test.go") {
		return false
	}
	for _, prefix := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
		rest, found := strings.CutPrefix(function.Name.Name, prefix)
		if !found {
			continue
		}
		if rest == "" {
			return true
		}
		first, _ := utf8.DecodeRuneInString(rest)
		return !unicode.IsLower(first)
	}
	return false
}

// openingName is the name a doc comment opens with, looking past an article.
func openingName(words []string) string {
	if len(words) > 1 && leadingArticles[words[0]] {
		return words[1]
	}
	return words[0]
}

// documentedDeclarations returns every doc comment a declaration carries,
// with the names each one is attached to. Imports are skipped: they declare no
// name a comment would be expected to open with.
//
// A grouped var or const block carries doc comments at TWO levels -- the
// block's own, and one on each specification inside it -- and returning only
// the block's left the inner ones unchecked. The tree already had one, on
// mackupVCSBuildErr inside the binaries block, so an insertion under it was
// invisible to this case: verified by sliding a declaration in there and
// watching the suite stay green.
//
// That is the third blind spot found in this guard. The first version saw only
// funcs and single-spec types; the second added grouped blocks but not the
// specs inside them. When adding to it, say what it still cannot see rather
// than reporting that it now sees everything -- that claim has been wrong
// twice.
func documentedDeclarations(declaration ast.Decl) []documented {
	switch d := declaration.(type) {
	case *ast.FuncDecl:
		return []documented{{names: []string{d.Name.Name}, doc: d.Doc}}
	case *ast.GenDecl:
		if d.Tok == token.IMPORT {
			return nil
		}
		found := []documented{}
		names := []string{}
		for _, spec := range d.Specs {
			specNames, specDoc := specNamesAndDoc(spec)
			names = append(names, specNames...)
			// A spec's own comment documents exactly that spec, so it is held
			// to the single-name convention even inside a block, where the
			// collective comment is not.
			if specDoc != nil && len(specNames) > 0 {
				found = append(found, documented{names: specNames, doc: specDoc, collective: len(specNames) > 1})
			}
		}
		return append(found, documented{names: names, doc: d.Doc, collective: d.Lparen.IsValid() || len(names) > 1})
	}
	return nil
}

// specNamesAndDoc returns the names one specification introduces and the doc
// comment written directly above it, inside its block.
func specNamesAndDoc(spec ast.Spec) ([]string, *ast.CommentGroup) {
	switch sp := spec.(type) {
	case *ast.TypeSpec:
		return []string{sp.Name.Name}, sp.Doc
	case *ast.ValueSpec:
		names := []string{}
		for _, name := range sp.Names {
			names = append(names, name.Name)
		}
		return names, sp.Doc
	}
	return nil, nil
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

	t.Run("mode changed with the bytes and the stamp left alone", func(t *testing.T) {
		// The shape that pins the mode field. chmod moves ctime, which
		// Snapshot does not record, and leaves both the bytes and the
		// modification time exactly where they were -- so the mode is the only
		// field that differs, and with it dropped from the record this
		// assertion passes over a file whose permissions changed. Verified:
		// rewriting the file record without the mode left the suite green.
		//
		// Not a hypothetical field to lose. Once link install lands
		// (macklebox-link-sync-83q.2), a dry-run or a rejected run that chmods
		// ~/.ssh/config from 0600 to 0644 is exactly what every "touched
		// nothing" promise in this suite exists to catch.
		world := NewWorld(t)
		path := world.WriteFile(".mackup.cfg", original, 0o600)
		before := world.Snapshot()
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatalf("changing the mode of %s: %v", path, err)
		}
		expectReported(t, world.captureReport(t, func() { world.ExpectUnchanged(before) }),
			world.SnapshotKey(".mackup.cfg"), "changed")
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
