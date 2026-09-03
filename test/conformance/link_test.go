//go:build conformance

// The link half of the suite, one command at a time as its ticket lands:
// `link install` as appspec/06-sync-operations.md specifies it, plus the
// promises of appspec/00-overview.md that only link mode makes -- promise 1
// (the indirection is transparent, so the home path must end up a SYMLINK and
// not a copy) and promise 3's fixed point in its link-mode form (an
// already-linked file is skipped).
//
// The habits are sync_test.go's, and for its reasons: cases sync a PROBE
// application whose file set they wrote themselves, and a case meaning "the
// program did the thing" asserts the thing on disk rather than the progress
// line, since the progress line is printed before the mutation.
//
// One habit is this file's own. `link install` is the first command that
// REMOVES something from the user's home directory, so a case about it asserts
// all three of the end state's parts -- the home path is a link, it resolves
// into the Mackup folder, and the Mackup folder holds the content -- rather
// than any one of them. A program that copied and did not delete, one that
// deleted and did not link, and one that linked to the wrong place each satisfy
// two of the three.
package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The literal shapes appspec/06 and appspec/07 give `link install`.
const (
	// linkVerb is the short progress form's word, and linkInstallLongVerb the
	// verbose four-line form's. They are DIFFERENT words for this one command
	// -- appspec/06 step 2 writes "Linking <f> ..." and, in verbose, "Backing
	// up\n  <home>\n  to\n  <mackup> ..." -- which is why both are named here
	// rather than one being derived from the other.
	linkVerb            = "Linking"
	linkInstallLongVerb = backupVerb

	// linkInstallReplacePrompt is appspec/07's "Replace an existing backup on
	// link install" question, minus the <type> and <mackup> it interpolates.
	linkInstallReplacePrompt = " already exists in the backup. Are you sure that you want to replace it?"

	// doingNothing opens all three verbose skip traces of appspec/06 step 1,
	// and the three constants after it are the tails that tell those traces
	// apart -- the trace is "keyed on the LinkState (already backed up /
	// broken link / does not exist)". Only the distinguishing tail is spelled
	// out for each, because the
	// wording is not a machine-read contract (appspec/07 names the three
	// literal tokens that are, and none of them is this), so a case asserts
	// that the program said WHICH state it skipped on, not that it said it in
	// these words.
	doingNothing       = "Doing nothing"
	doingNothingLinked = "already linked by Mackup"
	doingNothingBroken = "broken link"
	doingNothingAbsent = "does not exist"
)

// expectLinkedInto asserts the three parts of appspec/06's "Net effect" for one
// file: the home path is a symlink, it resolves to the mackup path, and the
// mackup path holds want as a real file.
//
// os.Lstat for the first, because os.Stat follows the link and would report the
// storage file's kind -- which is precisely the difference between a program
// that linked and one that merely left a copy in home. os.SameFile for the
// second rather than a string comparison of the readlink target, for the reason
// syncfs.AlreadyLinked gives: a storage root reached through a symlink spells
// the target differently and is still the same file.
func expectLinkedInto(t *testing.T, homePath, mackupPath, want string) {
	t.Helper()
	info, err := os.Lstat(homePath)
	if err != nil {
		t.Fatalf("stat %s: %v", homePath, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("%s is %s, want a symlink into the Mackup folder", homePath, info.Mode().Type())
		return
	}
	resolved, err := os.Stat(homePath)
	if err != nil {
		t.Fatalf("resolving %s: %v", homePath, err)
	}
	target, err := os.Stat(mackupPath)
	if err != nil {
		t.Fatalf("stat %s: %v", mackupPath, err)
	}
	if !os.SameFile(resolved, target) {
		got, _ := os.Readlink(homePath)
		t.Errorf("%s resolves to %s, want the Mackup copy at %s", homePath, got, mackupPath)
	}
	expectRealFile(t, mackupPath)
	expectContent(t, mackupPath, want)
}

// -- appspec/06 "`link install`": the net effect -----------------------------

func TestLinkInstallMovesTheHomeFileIntoStorageAndSymlinksItBack(t *testing.T) {
	// appspec/06 "Net effect": "each home config file becomes a symlink into
	// the Mackup folder, with the real content living in the Mackup folder
	// (permissions 0600/0700)". appspec/01 section 2 states what that means and
	// is the reason this is a different command from backup rather than a flag
	// on it: after backup the truth is the home file and storage holds a copy;
	// after this the truth is the STORAGE file and home is only a pointer.
	//
	// The mode is asserted on the storage copy and through Lstat, so the link's
	// own mode -- which a chmod follows to its target, and which no filesystem
	// lets the program set anyway -- is not what is being read.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", "home content\n", 0o644)

	world.Run("link", "install", probeKey).ExpectExit(0).ExpectSilentStderr().
		ExpectStdout(linkVerb + " .probrc ...")

	expectLinkedInto(t, world.Path(".probrc"), world.Mackup(".probrc"), "home content\n")
	expectMode(t, world.Mackup(".probrc"), 0o600)
}

func TestLinkInstallLinksADirectoryAndClampsTheWholeTree(t *testing.T) {
	// The same net effect for a directory, which appspec/06 admits at step 1
	// ("exists as a regular file OR DIRECTORY") and whose clamp appspec/06
	// "Permissions" states recursively: 0700 for directories and 0600 for the
	// files inside them.
	//
	// Worth its own case because the three operations behave differently on a
	// tree: syncfs.Copy recurses and merges, syncfs.Delete removes the tree,
	// and syncfs.Link clamps the target before pointing at it. A program that
	// linked the directory but left the home tree behind would still show a
	// link at the home path -- so the assertion is that the home path is a link
	// and NOT a directory, which Lstat is what distinguishes.
	world := newSyncWorld(t, ".probdir")
	world.WriteFile(".probdir/inner", "nested\n", 0o644)
	world.WriteFile(".probdir/sub/deeper", "deeper\n", 0o644)

	world.Run("link", "install", probeKey).ExpectExit(0).ExpectSilentStderr()

	// The whole directory is ONE managed path, so the link is at .probdir and
	// nowhere below it: the files inside are reached THROUGH that one link, and
	// asserting a link on each of them would be asserting a shape the program
	// is specified not to build.
	info, err := os.Lstat(world.Path(".probdir"))
	if err != nil {
		t.Fatalf("stat %s: %v", world.Path(".probdir"), err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("%s is %s, want a symlink: the home tree is moved, not copied", world.Path(".probdir"), info.Mode().Type())
	}
	resolved, err := os.Stat(world.Path(".probdir"))
	if err != nil {
		t.Fatalf("resolving %s: %v", world.Path(".probdir"), err)
	}
	target, err := os.Stat(world.Mackup(".probdir"))
	if err != nil {
		t.Fatalf("stat %s: %v", world.Mackup(".probdir"), err)
	}
	if !os.SameFile(resolved, target) {
		t.Errorf("%s does not resolve to the Mackup copy at %s", world.Path(".probdir"), world.Mackup(".probdir"))
	}

	// The tree landed whole, and the clamp of appspec/06 "Permissions" reached
	// every level of it rather than only the root it was handed.
	expectContent(t, world.Mackup(".probdir", "inner"), "nested\n")
	expectContent(t, world.Mackup(".probdir", "sub", "deeper"), "deeper\n")
	expectMode(t, world.Mackup(".probdir"), 0o700)
	expectMode(t, world.Mackup(".probdir", "inner"), 0o600)
	expectMode(t, world.Mackup(".probdir", "sub"), 0o700)
	expectMode(t, world.Mackup(".probdir", "sub", "deeper"), 0o600)
}

func TestASecondLinkInstallSkipsEveryFileAndChangesNothing(t *testing.T) {
	// appspec/06: "Idempotent: an already-linked file is skipped (step 1's
	// guard)", which is appspec/00 promise 3 in its link-mode form. The guard
	// is syncfs.AlreadyLinked, the one predicate of appspec/01 section 2.
	//
	// Both halves are asserted, because either alone passes over a broken
	// program: silence alone is what a run that skipped everything for the
	// wrong reason also produces, and an unchanged tree alone is what a
	// re-linking run that happened to land on the same bytes produces. The
	// snapshot carries mtimes, so a file rewritten with identical content is
	// still a change.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", "home content\n", 0o644)

	world.Run("link", "install", probeKey).ExpectExit(0).ExpectSilentStderr()

	before := world.Snapshot()
	result := world.Run("link", "install", probeKey).ExpectExit(0).ExpectSilentStderr()

	if strings.Contains(result.StdoutText(), linkVerb) {
		t.Errorf("the second link install printed %q, want nothing: every file is already linked", result.Stdout)
	}
	world.ExpectUnchanged(before)
}

func TestLinkInstallDryRunStopsAtTheProgressLineAndMutatesNothing(t *testing.T) {
	// appspec/06 step 2: "If dry-run, stop here for this file", and appspec/01
	// section 3's uniform rule -- dry-run "performs no copy, move, delete, or
	// symlink of any config file". For this command that is three mutations
	// rather than one, and the home DELETE is the one a user cannot undo, so
	// the snapshot is the assertion and the progress line only shows the run
	// got as far as deciding to act.
	//
	// Two files, on both of appspec/06's branches: one with no copy in storage
	// (step 4) and one with a copy already there (step 3, whose prompt lives
	// inside the mutation it guards). Stdin is at end-of-input, so a program
	// that asked would fail here rather than being quietly answered.
	world := newSyncWorld(t, ".fresh", ".conflict")
	world.WriteFile(".fresh", "only in home\n", 0o600)
	world.WriteFile(".conflict", "home side\n", 0o600)
	world.WriteMackup(".conflict", "storage side\n", 0o600)

	before := world.Snapshot()
	result := world.Run("--dry-run", "link", "install", probeKey).ExpectExit(0).ExpectSilentStderr()

	stdout := result.StdoutText()
	for _, relative := range []string{".fresh", ".conflict"} {
		if want := linkVerb + " " + relative + " ..."; !strings.Contains(stdout, want) {
			t.Errorf("--dry-run link install stdout = %q, want the progress line %q", result.Stdout, want)
		}
	}
	if strings.Contains(stdout, answerHint) {
		t.Errorf("--dry-run link install stdout = %q, want no prompt: dry-run stops at the progress line", result.Stdout)
	}
	world.ExpectUnchanged(before)
}

func TestLinkInstallDryRunStillRunsTheFolderCreationGate(t *testing.T) {
	// The one exception appspec/01 section 3 carves out names this command by
	// name: "dry-run does not suppress the startup 'create the storage
	// sub-folder' decision for backup / LINK INSTALL -- that gate runs before
	// the per-file loop and, under a force flag, will still create the folder".
	//
	// The backup half of the same sentence has its own case; this is the half
	// that says the rule is about the gate rather than about backup, and it is
	// the reason link install runs level 2 of appspec/01 section 4's lattice
	// rather than level 3.
	world, folder := newGateWorld(t, ".probrc")
	world.WriteFile(".probrc", "home\n", 0o600)

	world.Run("--dry-run", "--force", "link", "install", probeKey).ExpectExit(0).ExpectSilentStderr()

	if info, err := os.Stat(folder); err != nil || !info.IsDir() {
		t.Errorf("--dry-run --force link install left no Mackup folder at %s (%v): the environment gate is not a per-file mutation", folder, err)
	}
	expectAbsent(t, filepath.Join(folder, ".probrc"))
	expectRealFile(t, world.Path(".probrc"))
}

func TestLinkInstallCreatesTheMackupFolderOnYesAndRefusesWithoutIt(t *testing.T) {
	// appspec/01 section 4 puts link install on level 2 of the gate lattice --
	// "usable environment, then ENSURE the Mackup folder exists
	// (create-on-confirm)" -- alongside backup and not alongside restore. Both
	// answers, because the gate is one decision with two outcomes and a
	// program wired to level 3 instead would fail the run rather than offer.
	created, folder := newGateWorld(t, ".probrc")
	created.WriteFile(".probrc", "home\n", 0o600)

	created.RunWithInput("yes\n", "link", "install", probeKey).ExpectExit(0).ExpectSilentStderr()

	expectLinkedInto(t, created.Path(".probrc"), filepath.Join(folder, ".probrc"), "home\n")

	declined, folder := newGateWorld(t, ".probrc")
	declined.WriteFile(".probrc", "home\n", 0o600)
	before := declined.Snapshot()

	declined.RunWithInput("no\n", "link", "install", probeKey).
		ExpectFailureExit().ExpectStderrLine(noHomeRefusal)

	expectAbsent(t, folder)
	declined.ExpectUnchanged(before)
}

func TestLinkInstallRefusesAnUnknownApplicationBeforeAnyFolderOrPrompt(t *testing.T) {
	// appspec/06 "Environment gate per command": when an application is named
	// "its validity is checked BEFORE this gate, so an unknown app name fails
	// with `Unsupported application: <name>` (exit 1) before any folder is
	// created or prompt shown". The rule names link install explicitly.
	//
	// The folder's absence is the assertion the exit code cannot make: the run
	// exits 1 either way, and a program that gated first would have prompted --
	// with stdin at end-of-input, failing for a reason that looks the same from
	// the outside.
	world, folder := newGateWorld(t, ".probrc")
	world.WriteFile(".probrc", "home\n", 0o600)

	world.Run("link", "install", "frobnicate").ExpectExit(1).ExpectSilentStdout().
		ExpectStderrLine(unsupportedApplicationPrefix + "frobnicate")

	expectAbsent(t, folder)
	expectRealFile(t, world.Path(".probrc"))
}

// -- appspec/06 step 3: the replace-in-backup prompt -------------------------

func TestLinkInstallPromptsBeforeReplacingAnExistingBackupAndNamesIt(t *testing.T) {
	// appspec/06 step 3 and appspec/07's prompt list: "A <type> named <mackup>
	// already exists in the backup. Are you sure that you want to replace it?"
	//
	// The prompt carries no --force hint: appspec/07 gives that parenthetical
	// to backup's replace prompt alone, and this is the second prompt in the
	// program that could plausibly have inherited it.
	//
	// On yes, appspec/06's order is "delete the mackup copy, copy home->mackup,
	// delete the home file, create a symlink" -- so the storage copy ends up
	// holding the HOME content, which is what says the replace happened rather
	// than the home file being discarded in favour of the copy already there.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", "home wins\n", 0o600)
	world.WriteMackup(".probrc", "stale storage\n", 0o600)

	result := world.RunWithInput("yes\n", "link", "install", probeKey).
		ExpectExit(0).ExpectSilentStderr()

	stdout := result.StdoutText()
	if want := "A file named " + world.Mackup(".probrc") + linkInstallReplacePrompt; !strings.Contains(stdout, want) {
		t.Errorf("link install stdout = %q, want the replace prompt %q", result.Stdout, want)
	}
	if strings.Contains(stdout, forceHint) {
		t.Errorf("link install stdout = %q, want no %q: appspec/07 gives that hint to backup's prompt alone", result.Stdout, forceHint)
	}
	expectLinkedInto(t, world.Path(".probrc"), world.Mackup(".probrc"), "home wins\n")
}

func TestDecliningTheLinkInstallPromptLeavesBothSidesAloneAndExitsZero(t *testing.T) {
	// appspec/06 step 3: "On no: do nothing for this file." Nothing means all
	// three operations, and the home file is the one worth naming -- a program
	// that took the prompt to guard only the storage delete would answer "no"
	// and then move the home file anyway.
	//
	// Exit 0, because a declined prompt is a deliberate answer and appspec/00
	// promise 4 makes declining the default of the safety gate. The run carries
	// on to the next file in sorted order, which is what says the skip was
	// per-file rather than a stop.
	world := newSyncWorld(t, ".conflict", ".zfresh")
	world.WriteFile(".conflict", "home side\n", 0o600)
	world.WriteMackup(".conflict", "storage side\n", 0o600)
	world.WriteFile(".zfresh", "carried on\n", 0o600)

	world.RunWithInput("no\n", "link", "install", probeKey).ExpectExit(0).ExpectSilentStderr()

	expectRealFile(t, world.Path(".conflict"))
	expectContent(t, world.Path(".conflict"), "home side\n")
	expectContent(t, world.Mackup(".conflict"), "storage side\n")
	expectLinkedInto(t, world.Path(".zfresh"), world.Mackup(".zfresh"), "carried on\n")
}

func TestTheForceFlagsAnswerTheLinkInstallPromptWithoutShowingIt(t *testing.T) {
	// appspec/07: --force "pre-answers every prompt with yes: no prompt is
	// shown"; --force-no the same with the guarded action skipped. "Every
	// prompt" is the claim, and this is the newest one in the program -- the
	// place a second confirmation mechanism written inline would show up.
	//
	// Stdin is at end-of-input in both runs, so a program that asked would fail
	// rather than be answered.
	for _, test := range []struct {
		flag    string
		storage string
	}{
		{"--force", "home side\n"},
		{"--force-no", "storage side\n"},
	} {
		world := newSyncWorld(t, ".conflict")
		world.WriteFile(".conflict", "home side\n", 0o600)
		world.WriteMackup(".conflict", "storage side\n", 0o600)

		result := world.Run(test.flag, "link", "install", probeKey).
			ExpectExit(0).ExpectSilentStderr()

		if strings.Contains(result.StdoutText(), answerHint) {
			t.Errorf("%s link install stdout = %q, want no prompt shown", test.flag, result.Stdout)
		}
		expectContent(t, world.Mackup(".conflict"), test.storage)
	}
}

// -- appspec/06 step 1: the guard and its verbose traces ---------------------

func TestLinkInstallActsOnAHomeSymlinkThatPointsSomewhereElse(t *testing.T) {
	// Step 1's guard is "not already a symlink to ITS MACKUP PATH", not "not a
	// symlink". A home path pointing anywhere else is appspec/01 section 2's
	// Foreign state, it stats as a regular file through the link, and it is
	// acted on: the CONTENT is copied into storage and the home link is
	// replaced by one into the Mackup folder.
	//
	// The file the old link pointed at is left alone, which syncfs.Delete
	// guarantees by removing a symlink as the link rather than as its target --
	// the same property appspec/01 relies on to say "no transition ever deletes
	// the storage copy".
	world := newSyncWorld(t, ".probrc")
	elsewhere := world.WriteFile("elsewhere", "pointed at\n", 0o600)
	symlink(t, elsewhere, world.Path(".probrc"))

	world.Run("link", "install", probeKey).ExpectExit(0).ExpectSilentStderr()

	expectLinkedInto(t, world.Path(".probrc"), world.Mackup(".probrc"), "pointed at\n")
	expectRealFile(t, elsewhere)
	expectContent(t, elsewhere, "pointed at\n")
}

func TestLinkInstallSkipsAFileThatIsNotInHomeAndSaysNothingWithoutVerbose(t *testing.T) {
	// Step 1's other arm: a home path that is not there is skipped, and the
	// trace that says so is verbose-only. Silence is the contract on an
	// unscoped run over a 600-application catalog, where nearly every file is
	// absent.
	world := newSyncWorld(t, ".absent")

	before := world.Snapshot()
	world.Run("link", "install", probeKey).ExpectExit(0).
		ExpectSilentStdout().ExpectSilentStderr()

	world.ExpectUnchanged(before)
}

func TestVerboseSaysWhichStateLinkInstallDidNothingOn(t *testing.T) {
	// appspec/06 step 1: "verbose prints a 'Doing nothing ...' trace keyed on
	// the LinkState (already backed up / broken link / does not exist)". Three
	// keys, three states, and the point of the case is that they are told
	// apart: a program printing one trace for every skip satisfies a case that
	// only looked for "Doing nothing".
	//
	// The states are appspec/01 section 2's, derived by syncfs.StateOf, and the
	// three files are arranged to sit in one each. The already-linked one is
	// made by running the command, rather than by hand-building a symlink, so
	// that the state the program skips on is the state the program produces.
	world := newSyncWorld(t, ".alinked", ".mbroken", ".zabsent")
	world.WriteFile(".alinked", "linked\n", 0o600)
	world.Run("link", "install", probeKey).ExpectExit(0)
	symlink(t, world.Path("nowhere"), world.Path(".mbroken"))

	result := world.Run("--verbose", "link", "install", probeKey).
		ExpectExit(0).ExpectSilentStderr()

	stdout := result.StdoutText()
	for _, test := range []struct{ path, want string }{
		{world.Path(".alinked"), doingNothingLinked},
		{world.Path(".mbroken"), doingNothingBroken},
		{world.Path(".zabsent"), doingNothingAbsent},
	} {
		if !strings.Contains(stdout, doingNothing+"\n  "+test.path+"\n") {
			t.Errorf("--verbose link install stdout = %q, want a %q trace naming %s", result.Stdout, doingNothing, test.path)
		}
		if !strings.Contains(stdout, test.want) {
			t.Errorf("--verbose link install stdout = %q, want the trace for %s to say %q", result.Stdout, test.path, test.want)
		}
	}
}

func TestLinkInstallsVerboseProgressUsesTheBackupWordAndAbsolutePaths(t *testing.T) {
	// appspec/06 step 2 gives this command two different words: "Linking <f>
	// ..." short, and the four-line "Backing up\n  <home>\n  to\n  <mackup>
	// ..." in verbose. The verbose word being backup's is the surprising half
	// and is what this case pins -- a reimplementation that "corrected" it to
	// "Linking" would be changing a line appspec/06 writes out.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", "home\n", 0o600)

	result := world.Run("--verbose", "link", "install", probeKey).
		ExpectExit(0).ExpectSilentStderr()

	want := linkInstallLongVerb + "\n  " + world.Path(".probrc") + "\n  to\n  " + world.Mackup(".probrc") + " ..."
	if !strings.Contains(result.StdoutText(), want) {
		t.Errorf("--verbose link install stdout = %q, want the four-line progress form %q", result.Stdout, want)
	}
	if strings.Contains(result.StdoutText(), linkVerb+" .probrc ...") {
		t.Errorf("--verbose link install stdout = %q, want the long form INSTEAD of the short one", result.Stdout)
	}
}

// -- appspec/01 section 5: link commands fail hard mid-run -------------------

func TestAFailureInsideLinkInstallStopsTheRunMidWay(t *testing.T) {
	// appspec/01 section 5 states the asymmetry as contract: "backup/restore
	// degrade gracefully and report; link commands fail hard mid-run", leaving
	// "earlier files transitioned and later files untouched". appspec/07's error
	// table gives it a row -- "Failure inside a link operation | stderr |
	// nonzero | uncaught error, run stops mid-way | unguarded".
	//
	// Three files in sorted order, so all three clauses are observable in one
	// run: the first is transitioned, the second fails, and the THIRD is the
	// assertion that distinguishes this contract from backup's -- under the
	// partial-failure contract it would have been acted on and named in a
	// summary.
	//
	// The failure is arranged by putting a regular file where the storage
	// destination's parent belongs, which is portable and needs no permission
	// games: the stat of the mackup path returns ENOTDIR, which is neither
	// absence nor a copy, so the guard ahead of step 3 stops the run.
	world := newSyncWorld(t, ".afirst", ".mdir/inner", ".zlast")
	world.WriteFile(".afirst", "transitioned\n", 0o600)
	world.WriteFile(".mdir/inner", "cannot land\n", 0o600)
	world.WriteFile(".zlast", "untouched\n", 0o600)
	world.WriteMackup(".mdir", "a file where the folder belongs\n", 0o600)

	result := world.Run("link", "install", probeKey).ExpectFailureExit()

	if stderr := result.StderrText(); !strings.Contains(stderr, world.Mackup(".mdir", "inner")) {
		t.Errorf("link install stderr = %q, want a diagnostic naming %s", result.Stderr, world.Mackup(".mdir", "inner"))
	}
	if strings.Contains(result.StderrText(), "file(s) could not be copied") {
		t.Errorf("link install stderr = %q, want no end-of-run summary: appspec/01 section 5 gives link commands no aggregation path", result.Stderr)
	}
	// Earlier files transitioned; later files untouched.
	expectLinkedInto(t, world.Path(".afirst"), world.Mackup(".afirst"), "transitioned\n")
	expectRealFile(t, world.Path(".zlast"))
	expectAbsent(t, world.Mackup(".zlast"))
}

func TestLinkInstallLeavesTheHomeFileWhereItIsUntilTheCopyHasLanded(t *testing.T) {
	// appspec/01 section 2 fixes the per-file order as copy home->mackup,
	// delete home, symlink -- and names the window between the last two as "the
	// dangerous non-atomic window". The order's first half is what this case
	// can observe from outside: when the COPY is the operation that fails, the
	// home file must still be there afterwards.
	//
	// A program that deleted first and copied second would pass every net-effect
	// case in this file and lose the user's file on the one run that fails. The
	// failure is arranged inside the copy itself -- a source directory holding a
	// dangling symlink, which syncfs.Copy refuses as "not a regular file or
	// directory" -- so the mackup path stats cleanly as absent and the guard
	// ahead of step 3 does not fire.
	world := newSyncWorld(t, ".probdir")
	world.WriteFile(".probdir/inner", "real\n", 0o600)
	symlink(t, world.Path("nowhere"), world.Path(".probdir", "dangling"))

	world.Run("link", "install", probeKey).ExpectFailureExit()

	if _, err := os.Lstat(world.Path(".probdir")); err != nil {
		t.Errorf("stat %s after a failed copy: %v -- the home tree must survive a copy that did not land", world.Path(".probdir"), err)
	}
	expectContent(t, world.Path(".probdir", "inner"), "real\n")
}

func TestEndOfInputAtTheLinkInstallPromptEndsTheRunUnguarded(t *testing.T) {
	// appspec/07: a prompt reached with no force flag and stdin at end of input
	// "cannot obtain a valid answer and terminates with a nonzero exit (an
	// unhandled end-of-input condition -- the unguarded regime)". Not an
	// implicit no: the file is left exactly as it was, and the run does not
	// carry on to the next file.
	world := newSyncWorld(t, ".conflict", ".zfresh")
	world.WriteFile(".conflict", "home side\n", 0o600)
	world.WriteMackup(".conflict", "storage side\n", 0o600)
	world.WriteFile(".zfresh", "not reached\n", 0o600)

	world.Run("link", "install", probeKey).ExpectFailureExit()

	expectRealFile(t, world.Path(".conflict"))
	expectContent(t, world.Mackup(".conflict"), "storage side\n")
	expectRealFile(t, world.Path(".zfresh"))
	expectAbsent(t, world.Mackup(".zfresh"))
}

// -- appspec/00 promise 2 and appspec/01 section 2: what link install is for --

func TestBackupSkipsWhatLinkInstallLinkedWhichIsTheOnePredicate(t *testing.T) {
	// appspec/01 section 2: the already-linked predicate "is used identically by
	// four operations", and backup's link-skip and link install's guard are two
	// of them. Running one command after the other is the only way to observe
	// that they agree, and disagreement is the failure appspec/01 says a
	// four-times-coded check produces.
	//
	// It is also the state model's claim in miniature: after link install the
	// file is Linked, and backup's transition out of Linked is a no-op.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", "home content\n", 0o644)

	world.Run("link", "install", probeKey).ExpectExit(0).ExpectSilentStderr()
	before := world.Snapshot()

	result := world.Run("--verbose", "backup", probeKey).ExpectExit(0).ExpectSilentStderr()

	if !strings.Contains(result.StdoutText(), "already linked") {
		t.Errorf("backup after link install stdout = %q, want the link-skip trace", result.Stdout)
	}
	world.ExpectUnchanged(before)
}

func TestLinkInstallStoresContentAtTheSameHomeRelativePath(t *testing.T) {
	// appspec/00 promise 7: everything in the shared folder sits "at the same
	// home-relative path it had under the home directory, with no
	// machine-specific absolute path baked in". For link install the promise
	// has a second edge the copy commands do not: the symlink it leaves behind
	// DOES hold an absolute path, and it must point into the Mackup folder
	// rather than anywhere else, so the portable half is the storage layout and
	// the machine-specific half is the link.
	world := newSyncWorld(t, ".config/nested/probrc")
	world.WriteFile(".config/nested/probrc", "nested\n", 0o600)

	world.Run("link", "install", probeKey).ExpectExit(0).ExpectSilentStderr()

	expectLinkedInto(t,
		world.Path(".config", "nested", "probrc"),
		world.Mackup(".config", "nested", "probrc"),
		"nested\n")

	target, err := os.Readlink(world.Path(".config", "nested", "probrc"))
	if err != nil {
		t.Fatalf("reading the link: %v", err)
	}
	if !filepath.IsAbs(target) {
		t.Errorf("the link points at %q, want an absolute path: a relative target resolves against the link's own directory", target)
	}
}
