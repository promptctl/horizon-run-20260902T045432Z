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
	"fmt"
	"io/fs"
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

	// joinVerb is `link`'s short progress word. A third spelling of a
	// neighbouring idea -- restore says "Recovering", link install says
	// "Linking" -- and appspec/06 step 2 writes it, so it stands.
	//
	// It is also the word `link` and restore are told apart by in
	// argv_test.go's invocation table, which is why it is a constant rather
	// than a literal typed at each use.
	joinVerb = "Restoring"

	// linkReplaceQuestion is appspec/07's "Replace an existing home file on
	// link" question, with the <type> and <home> it interpolates left as verbs
	// for fmt: unlike link install's, this prompt puts a path in the MIDDLE of
	// the sentence, so a prefix/suffix pair would assert less than the whole.
	linkReplaceQuestion = "You already have a %s at %s. Do you want to replace it with your backup?"
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
	//
	// What this case does NOT observe is that guard. Remove it and the run
	// reaches syncfs.Copy, which fails at the same ENOTDIR one call later with
	// the same exit code, the same untouched third file and the same path in
	// the diagnostic -- every assertion below still passes. That is
	// TestAMackupPathThatCannotBeInspectedStopsBeforeTheCopyRatherThanInsideIt's
	// job, on this same fixture; the two are separate because the observable
	// that tells the guard from the copy is not one the fail-hard contract
	// cares about.
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

func TestAMackupPathThatCannotBeInspectedStopsBeforeTheCopyRatherThanInsideIt(t *testing.T) {
	// appspec/06 splits this command's steps 3 and 4 on whether "a copy already
	// exists at the mackup path". A stat of that path which fails for a reason
	// OTHER than its absence has answered neither question, so the procedure may
	// not take either branch -- and the branch it would fall into is step 4,
	// which copies home over that path (syncfs.Copy does not require an absent
	// destination) and then DELETES THE HOME FILE. The prompt of step 3 is the
	// only thing between an uninspectable storage copy and its silent
	// replacement by a file whose original is then removed.
	//
	// The fixture is TestAFailureInsideLinkInstallStopsTheRunMidWay's -- a
	// regular file where the mackup path's parent belongs, which is ENOTDIR and
	// not ENOENT -- and this case exists BECAUSE that one cannot see the guard.
	// Both spellings exit non-zero and both name the mackup path, since a copy
	// that reaches syncfs.Copy fails at the same ENOTDIR one call later. Removing
	// the guard leaves that case entirely green.
	//
	// What tells them apart is WHICH path the diagnostic names. The guard has
	// only looked at storage, so it names storage alone; a failure inside the
	// copy names both ends of the copy it attempted. Asserting that the home
	// path is absent from stderr is therefore the assertion that the run stopped
	// before it had a source to name -- the observable form of "no comparison,
	// no prompt, no write was attempted".
	//
	// The stronger shape -- an uninspectable mackup path that the copy could
	// nonetheless write over, so that the home file is actually lost -- is not
	// constructible: a stat of a leaf needs only search permission on its
	// parent, so nothing can deny the stat while still admitting the write.
	// sync_test.go's TestADestinationThatCannotBeInspectedIsAFailureAndNotAn-
	// Absence records the same limit for the copy commands and asserts the
	// predicate instead, which is what this does.
	world := newSyncWorld(t, ".mdir/inner")
	world.WriteFile(".mdir/inner", "must not be touched\n", 0o600)
	world.WriteMackup(".mdir", "a file where the folder belongs\n", 0o600)

	result := world.Run("link", "install", probeKey).ExpectFailureExit()

	if stderr := result.StderrText(); !strings.Contains(stderr, world.Mackup(".mdir", "inner")) {
		t.Errorf("link install stderr = %q, want a diagnostic naming the storage path %s it could not inspect", result.Stderr, world.Mackup(".mdir", "inner"))
	}
	if stderr := result.StderrText(); strings.Contains(stderr, world.Path(".mdir", "inner")) {
		t.Errorf("link install stderr = %q, want it to name the storage path ALONE: naming %s too means the run reached the copy instead of stopping at the inspection", result.Stderr, world.Path(".mdir", "inner"))
	}
	// Nothing was attempted: the home file is still the real one and no storage
	// copy was made beside the file that is blocking the path.
	expectRealFile(t, world.Path(".mdir", "inner"))
	expectContent(t, world.Path(".mdir", "inner"), "must not be touched\n")
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

// -- appspec/06 "`link`": the second entry door ------------------------------
//
// The cases below are `link`, and the habit this section adds to the file's is
// that a case about it asserts something about the STORAGE side too. appspec/06
// "Net effect" ends "the Mackup copies are not modified", and appspec/00's
// two-entry-doors section makes that the difference between this command and
// link install rather than a detail of it: a `link` that had been folded into
// link install would overwrite the first machine's file with whatever happened
// to be at the second machine's home path, and every assertion about the home
// path alone would still pass.

// expectLinkedIntoUnchangedStorage is expectLinkedInto plus the half that is
// this command's own: the storage copy still holds want, as a real file, having
// been read and never written.
func expectLinkedIntoUnchangedStorage(t *testing.T, homePath, mackupPath, want string) {
	t.Helper()
	expectLinkedInto(t, homePath, mackupPath, want)
	expectMode(t, mackupPath, 0o600)
}

// loosen sets a path's permission bits directly, for the storage fixtures whose
// point is that the clamp of appspec/06 "Permissions" has something to do.
//
// It exists because every other way this suite creates a file lands on 0600 or
// 0700 already -- WriteMackup's parents are made 0700 and the program's own
// primitives clamp -- so a case that wants to watch `link` narrow a
// group-readable storage file has to widen one first. That is the arrangement a
// second machine actually meets: the file arrives through a sync client with
// the modes it was written with somewhere else.
func loosen(t *testing.T, path string, mode fs.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("loosening %s to %04o: %v", path, mode, err)
	}
}

func TestLinkSymlinksAStorageFileIntoAnEmptyHomeAndLeavesStorageAlone(t *testing.T) {
	// appspec/06 `link` step 4 -- "if nothing exists at the home path: create
	// the symlink directly" -- and its "Net effect": "each home config path
	// becomes a symlink into the Mackup folder, USING CONTENT ALREADY PRESENT
	// in the Mackup folder. The Mackup copies are not modified."
	//
	// This is the second machine's whole story in one file: storage has the
	// content, home has nothing, and afterwards home points at storage. Nothing
	// was copied in either direction, which is what the content assertion on
	// the storage side says -- a program that had copied home over storage
	// first would have had nothing to copy and would pass a home-side check
	// alone.
	world := newSyncWorld(t, ".probrc")
	world.WriteMackup(".probrc", "from the first machine\n", 0o600)

	world.Run("link", probeKey).ExpectExit(0).ExpectSilentStderr().
		ExpectStdout(joinVerb + " .probrc ...")

	expectLinkedIntoUnchangedStorage(t, world.Path(".probrc"), world.Mackup(".probrc"), "from the first machine\n")
}

func TestLinkMovesNothingOutOfHomeWhenItReplacesAHomeFile(t *testing.T) {
	// appspec/00's two entry doors, stated as the one observation that tells
	// them apart. `link` "only CREATES THE SYMLINKS pointing at files already
	// in the shared folder; it moves nothing out of home", where link install
	// on this same fixture would have copied the home content into storage
	// first.
	//
	// So the two sides hold DIFFERENT content and the assertion is that
	// storage's survived. Run under --force, because the prompt is not what
	// this case is about and its own case is below; the point is what the yes
	// arm does, which appspec/06 step 3 gives as "delete the home path, then
	// create a symlink" -- two operations, and neither of them a copy.
	//
	// A program that had collapsed the two doors passes every other case in
	// this section and fails here, which is why it is written even though the
	// case above already asserts the storage content: there, storage is the
	// only content in the world.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", "the second machine's own\n", 0o600)
	world.WriteMackup(".probrc", "the shared one\n", 0o600)

	world.Run("--force", "link", probeKey).ExpectExit(0).ExpectSilentStderr()

	expectLinkedIntoUnchangedStorage(t, world.Path(".probrc"), world.Mackup(".probrc"), "the shared one\n")
}

func TestLinkClampsTheStorageTreeItPointsAtWithoutRewritingIt(t *testing.T) {
	// Two halves of appspec/06 that only meet under this command.
	//
	// "link(target, link_path) ... Before linking, target's permissions are set
	// recursively", with "Permissions (clamped on every write)" giving the
	// modes: 0700 for directories, 0600 for regular files, RECURSIVELY. Every
	// other command clamps a path its own copy has just created, where the
	// create modes already gave 0600/0700 -- so this is the one place in the
	// program where the clamp meets a tree it did not write and has something
	// to change.
	//
	// And "the Mackup copies are not modified", which is about CONTENT: a
	// storage tree comes back with narrower modes and the same bytes. The two
	// assertions together are the claim; the mode half alone is satisfied by a
	// program that rewrote the tree, and the content half alone by one that
	// never clamped.
	//
	// The fixture is a second machine's storage as a sync client leaves it:
	// group- and world-readable, because the account that wrote it on the first
	// machine had a different umask.
	world := newSyncWorld(t, ".probdir")
	world.WriteMackup(".probdir/inner", "nested\n", 0o644)
	world.WriteMackup(".probdir/sub/deeper", "deeper\n", 0o644)
	loosen(t, world.Mackup(".probdir", "sub"), 0o755)
	loosen(t, world.Mackup(".probdir"), 0o755)

	world.Run("link", probeKey).ExpectExit(0).ExpectSilentStderr()

	// The link is at .probdir and nowhere below it: the tree is reached
	// THROUGH that one link, which is the shape appspec/06 specifies.
	info, err := os.Lstat(world.Path(".probdir"))
	if err != nil {
		t.Fatalf("stat %s: %v", world.Path(".probdir"), err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("%s is %s, want a symlink into the Mackup folder", world.Path(".probdir"), info.Mode().Type())
	}

	expectContent(t, world.Mackup(".probdir", "inner"), "nested\n")
	expectContent(t, world.Mackup(".probdir", "sub", "deeper"), "deeper\n")
	expectMode(t, world.Mackup(".probdir"), 0o700)
	expectMode(t, world.Mackup(".probdir", "inner"), 0o600)
	expectMode(t, world.Mackup(".probdir", "sub"), 0o700)
	expectMode(t, world.Mackup(".probdir", "sub", "deeper"), 0o600)
}

func TestASecondLinkSkipsEveryFileAndChangesNothing(t *testing.T) {
	// appspec/06 "Idempotency and ordering guarantees": "link install / link:
	// an already-linked file is skipped", which is appspec/00 promise 3 at the
	// second door. The guard is syncfs.AlreadyLinked, the one predicate of
	// appspec/01 section 2 and its third call site.
	//
	// Both halves, for the reason the link install version of this case gives:
	// silence alone is what a run that skipped everything for the wrong reason
	// produces, and an unchanged tree alone is what a re-linking run that
	// happened to recreate the same link produces. The snapshot carries mtimes,
	// so a link removed and remade is a change.
	world := newSyncWorld(t, ".probrc")
	world.WriteMackup(".probrc", "shared\n", 0o600)

	world.Run("link", probeKey).ExpectExit(0).ExpectSilentStderr()

	before := world.Snapshot()
	result := world.Run("link", probeKey).ExpectExit(0).ExpectSilentStderr()

	if strings.Contains(result.StdoutText(), joinVerb) {
		t.Errorf("the second link printed %q, want nothing: every file is already linked", result.Stdout)
	}
	world.ExpectUnchanged(before)
}

func TestLinkSkipsAFileThatIsNotInStorageAndSaysNothingWithoutVerbose(t *testing.T) {
	// appspec/06 step 1's first condition: "act only if ... the mackup path
	// exists as a regular file or directory". This is the ordinary state of a
	// second machine whose sync client has not brought a file down yet, and
	// the home file beside it is the assertion that matters -- a `link` that
	// treated a missing storage copy as something to create would delete or
	// overwrite a real home file to make a dangling link.
	//
	// Silence is the contract, for the reason link install's version gives: on
	// an unscoped run over a 600-application catalog nearly every file is
	// absent on both sides.
	world := newSyncWorld(t, ".absent", ".zreal")
	world.WriteFile(".zreal", "the user's own\n", 0o600)

	before := world.Snapshot()
	world.Run("link", probeKey).ExpectExit(0).
		ExpectSilentStdout().ExpectSilentStderr()

	world.ExpectUnchanged(before)
	expectRealFile(t, world.Path(".zreal"))
}

func TestVerboseSaysWhyLinkDidNothingOnEachSkippedFile(t *testing.T) {
	// appspec/06 step 1: "If any condition fails, verbose prints a 'Doing
	// nothing ...' trace and nothing happens." The two conditions a run can
	// fail on either platform are the two rows below, and they are told apart
	// -- a program printing one trace for every skip satisfies a case that only
	// looked for "Doing nothing".
	//
	// The traces name DIFFERENT paths on purpose, and that is the part worth
	// pinning rather than the words. The missing-copy trace names the storage
	// path, because storage is the side that failed the condition and the
	// question a user has is which file their sync client has not brought down;
	// the already-linked trace names the home path, because that is where the
	// link the program found is. A trace that named the same end in both cases
	// would be telling the user which file rather than which side.
	//
	// The already-linked file is put in that state by RUNNING the command
	// rather than by building a symlink by hand, so the state the program skips
	// on is the state the program produces.
	world := newSyncWorld(t, ".alinked", ".zabsent")
	world.WriteMackup(".alinked", "shared\n", 0o600)
	world.Run("link", probeKey).ExpectExit(0)

	result := world.Run("--verbose", "link", probeKey).
		ExpectExit(0).ExpectSilentStderr()

	stdout := result.StdoutText()
	for _, test := range []struct{ path, want string }{
		{world.Path(".alinked"), doingNothingLinked},
		{world.Mackup(".zabsent"), doingNothingAbsent},
	} {
		if !strings.Contains(stdout, doingNothing+"\n  "+test.path+"\n") {
			t.Errorf("--verbose link stdout = %q, want a %q trace naming %s", result.Stdout, doingNothing, test.path)
		}
		if !strings.Contains(stdout, test.want) {
			t.Errorf("--verbose link stdout = %q, want the trace for %s to say %q", result.Stdout, test.path, test.want)
		}
	}
	if strings.Contains(stdout, doingNothing+"\n  "+world.Path(".zabsent")+"\n") {
		t.Errorf("--verbose link stdout = %q, want the missing-copy trace to name the STORAGE path, not the home path %s", result.Stdout, world.Path(".zabsent"))
	}
}

func TestLinksVerboseProgressUsesItsOwnThreeLineShape(t *testing.T) {
	// appspec/06 step 2 writes this command's verbose progress as "Restoring\n
	// linking <home>\n  to      <mackup> ...". Three lines, where backup,
	// restore and link install print four; the destination sits on the "to"
	// line rather than below it, and the run of spaces aligns the two paths
	// under each other.
	//
	// That shape is why the executor's progress form carries a template rather
	// than a verb, so this case is what stops the generalisation from being
	// quietly undone -- a reimplementation that "unified" the five commands'
	// verbose output would be flattening a difference appspec/06 writes out.
	world := newSyncWorld(t, ".probrc")
	world.WriteMackup(".probrc", "shared\n", 0o600)

	result := world.Run("--verbose", "link", probeKey).
		ExpectExit(0).ExpectSilentStderr()

	want := joinVerb + "\n  linking " + world.Path(".probrc") + "\n  to      " + world.Mackup(".probrc") + " ..."
	if !strings.Contains(result.StdoutText(), want) {
		t.Errorf("--verbose link stdout = %q, want the three-line progress form %q", result.Stdout, want)
	}
	if strings.Contains(result.StdoutText(), joinVerb+" .probrc ...") {
		t.Errorf("--verbose link stdout = %q, want the long form INSTEAD of the short one", result.Stdout)
	}
}

func TestLinkDryRunStopsAtTheProgressLineAndMutatesNothing(t *testing.T) {
	// appspec/06 step 2: "If dry-run, stop here for this file", and appspec/01
	// section 3's uniform rule -- dry-run "performs no copy, move, delete, or
	// symlink of any config file".
	//
	// Two files, on both of appspec/06's branches: one with nothing at the home
	// path (step 4) and one with a home file already there (step 3, whose
	// prompt lives inside the mutation it guards). Stdin is at end-of-input, so
	// a program that asked would fail here rather than being quietly answered.
	world := newSyncWorld(t, ".conflict", ".fresh")
	world.WriteMackup(".fresh", "shared\n", 0o600)
	world.WriteMackup(".conflict", "shared too\n", 0o600)
	world.WriteFile(".conflict", "the user's own\n", 0o600)

	before := world.Snapshot()
	result := world.Run("--dry-run", "link", probeKey).ExpectExit(0).ExpectSilentStderr()

	stdout := result.StdoutText()
	for _, relative := range []string{".fresh", ".conflict"} {
		if want := joinVerb + " " + relative + " ..."; !strings.Contains(stdout, want) {
			t.Errorf("--dry-run link stdout = %q, want the progress line %q", result.Stdout, want)
		}
	}
	if strings.Contains(stdout, answerHint) {
		t.Errorf("--dry-run link stdout = %q, want no prompt: dry-run stops at the progress line", result.Stdout)
	}
	world.ExpectUnchanged(before)
}

func TestLinkRequiresTheMackupFolderAndDoesNotOfferToCreateIt(t *testing.T) {
	// appspec/06 "Environment gate per command" puts `link` with restore and
	// link uninstall, not with backup and link install: "require the Mackup
	// folder to ALREADY EXIST -- if absent, fatal error naming the missing
	// Mackup folder (with a hint to back up or sync first) and exit 1". That is
	// level 3 of appspec/01 section 4's lattice.
	//
	// It is the shape of the command rather than a detail of it: this is the
	// join-an-existing-sync door, so a machine with no Mackup folder has
	// nothing to join, and a freshly created empty one would turn that into a
	// run which links nothing and exits 0.
	//
	// stdout is asserted silent because the failure mode of a program wired to
	// level 2 instead is a PROMPT, and with stdin at end-of-input that also
	// exits non-zero -- the exit code alone cannot tell the two apart.
	world, folder := newGateWorld(t, ".probrc")
	world.WriteFile(".probrc", "the user's own\n", 0o600)
	before := world.Snapshot()

	world.Run("link", probeKey).ExpectExit(1).ExpectSilentStdout().
		ExpectStderr(missingFolderError + folder)

	expectAbsent(t, folder)
	world.ExpectUnchanged(before)
}

func TestLinkRefusesAnUnknownApplicationBeforeTheGate(t *testing.T) {
	// appspec/06 "Environment gate per command": when an application is named
	// "its validity is checked BEFORE this gate, so an unknown app name fails
	// with `Unsupported application: <name>` (exit 1) before any folder is
	// created or prompt shown". The rule names `link` among the five.
	//
	// Both refusals exit 1, so the assertion is WHICH diagnostic came out: a
	// program that gated first would have reported the missing Mackup folder,
	// which is a true statement about a run the user never asked for.
	world, folder := newGateWorld(t, ".probrc")

	result := world.Run("link", "frobnicate").ExpectExit(1).ExpectSilentStdout().
		ExpectStderrLine(unsupportedApplicationPrefix + "frobnicate")

	if strings.Contains(result.StderrText(), missingFolderError) {
		t.Errorf("link frobnicate stderr = %q, want the unsupported-application refusal alone: the name is checked before the gate", result.Stderr)
	}
	expectAbsent(t, folder)
}

// -- appspec/06 step 3: the replace-in-home prompt ---------------------------

func TestLinkPromptsBeforeReplacingAHomeFileAndNamesIt(t *testing.T) {
	// appspec/06 step 3 and appspec/07's prompt list: "You already have a
	// <type> at <home>. Do you want to replace it with your backup?"
	//
	// It names the HOME path where link install's names the mackup path,
	// because the thing about to be destroyed is the home file. And it carries
	// no --force hint: appspec/07 gives that parenthetical to backup's prompt
	// alone, and this is the third prompt in the program that could plausibly
	// have inherited it.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", "the user's own\n", 0o600)
	world.WriteMackup(".probrc", "shared\n", 0o600)

	result := world.RunWithInput("yes\n", "link", probeKey).
		ExpectExit(0).ExpectSilentStderr()

	stdout := result.StdoutText()
	if want := fmt.Sprintf(linkReplaceQuestion, "file", world.Path(".probrc")); !strings.Contains(stdout, want) {
		t.Errorf("link stdout = %q, want the replace prompt %q", result.Stdout, want)
	}
	if strings.Contains(stdout, forceHint) {
		t.Errorf("link stdout = %q, want no %q: appspec/07 gives that hint to backup's prompt alone", result.Stdout, forceHint)
	}
	expectLinkedIntoUnchangedStorage(t, world.Path(".probrc"), world.Mackup(".probrc"), "shared\n")
}

func TestDecliningTheLinkPromptLeavesTheHomeFileAloneAndExitsZero(t *testing.T) {
	// appspec/06 step 3: "On no: do nothing for this file." The home file is
	// still a real file holding its own content, which is the whole of what the
	// user was protecting by answering no.
	//
	// Exit 0, because a declined prompt is a deliberate answer and appspec/00
	// promise 4 makes declining the default of the safety gate. The run carries
	// on to the next file in sorted order, which is what says the skip was
	// per-file rather than a stop.
	world := newSyncWorld(t, ".conflict", ".zfresh")
	world.WriteFile(".conflict", "the user's own\n", 0o600)
	world.WriteMackup(".conflict", "shared\n", 0o600)
	world.WriteMackup(".zfresh", "carried on\n", 0o600)

	world.RunWithInput("no\n", "link", probeKey).ExpectExit(0).ExpectSilentStderr()

	expectRealFile(t, world.Path(".conflict"))
	expectContent(t, world.Path(".conflict"), "the user's own\n")
	expectContent(t, world.Mackup(".conflict"), "shared\n")
	expectLinkedIntoUnchangedStorage(t, world.Path(".zfresh"), world.Mackup(".zfresh"), "carried on\n")
}

func TestTheForceFlagsAnswerTheLinkPromptWithoutShowingIt(t *testing.T) {
	// appspec/07: --force "pre-answers every prompt with yes: no prompt is
	// shown"; --force-no the same with the guarded action skipped. "Every
	// prompt" is the claim, and this is the newest one in the program.
	//
	// Stdin is at end-of-input in both runs, so a program that asked would fail
	// rather than be answered. The home path is what differs between the two
	// answers: a link on yes, the user's own real file on no.
	for _, test := range []struct {
		flag   string
		linked bool
	}{
		{"--force", true},
		{"--force-no", false},
	} {
		world := newSyncWorld(t, ".conflict")
		world.WriteFile(".conflict", "the user's own\n", 0o600)
		world.WriteMackup(".conflict", "shared\n", 0o600)

		result := world.Run(test.flag, "link", probeKey).
			ExpectExit(0).ExpectSilentStderr()

		if strings.Contains(result.StdoutText(), answerHint) {
			t.Errorf("%s link stdout = %q, want no prompt shown", test.flag, result.Stdout)
		}
		if test.linked {
			expectLinkedIntoUnchangedStorage(t, world.Path(".conflict"), world.Mackup(".conflict"), "shared\n")
			continue
		}
		expectRealFile(t, world.Path(".conflict"))
		expectContent(t, world.Path(".conflict"), "the user's own\n")
	}
}

func TestLinkReplacesAForeignHomeSymlinkAsALinkAndSparesItsTarget(t *testing.T) {
	// Two claims that meet on one fixture.
	//
	// appspec/07: "<type> is one of file, folder, or link, describing the
	// EXISTING path" -- so a home path that is a symlink to somewhere else is
	// prompted about as a "link". typeNoun reads an Lstat result for exactly
	// this reason: "You already have a link at ~/.probrc" tells the user
	// something a claim about a file does not, and the thing being replaced is
	// the link.
	//
	// And appspec/06's delete semantics: "a symlink is removed as the link, not
	// its target". The file the old link pointed at is left alone, which is the
	// same property appspec/01 section 2 relies on to say no transition ever
	// deletes the storage copy -- here protecting a file of the user's that is
	// not in the sync at all.
	world := newSyncWorld(t, ".probrc")
	world.WriteMackup(".probrc", "shared\n", 0o600)
	elsewhere := world.WriteFile("elsewhere", "pointed at\n", 0o600)
	symlink(t, elsewhere, world.Path(".probrc"))

	result := world.RunWithInput("yes\n", "link", probeKey).
		ExpectExit(0).ExpectSilentStderr()

	if want := fmt.Sprintf(linkReplaceQuestion, "link", world.Path(".probrc")); !strings.Contains(result.StdoutText(), want) {
		t.Errorf("link stdout = %q, want the prompt to call the existing path a link: %q", result.Stdout, want)
	}
	expectLinkedIntoUnchangedStorage(t, world.Path(".probrc"), world.Mackup(".probrc"), "shared\n")
	expectRealFile(t, elsewhere)
	expectContent(t, elsewhere, "pointed at\n")
}

// -- appspec/01 section 5: link commands fail hard mid-run -------------------

func TestAFailureInsideLinkStopsTheRunMidWay(t *testing.T) {
	// appspec/01 section 5 and appspec/07's "Failure inside a link operation"
	// row, for the second door: "uncaught error, run stops mid-way", leaving
	// earlier files transitioned and later files untouched.
	//
	// Three files in sorted order, so all three clauses are observable in one
	// run. The third is the assertion that distinguishes this contract from
	// backup's -- under the partial-failure contract it would have been acted
	// on and named in an end-of-run summary.
	//
	// The failure is arranged by putting a regular file where the home
	// destination's parent belongs, which is portable and needs no permission
	// games: the storage side is a directory holding one file, so the home path
	// the program must create is under $HOME/.mdir, and $HOME/.mdir is a file.
	// Every syscall through it returns ENOTDIR.
	world := newSyncWorld(t, ".afirst", ".mdir/inner", ".zlast")
	world.WriteMackup(".afirst", "transitioned\n", 0o600)
	world.WriteMackup(".mdir/inner", "cannot land\n", 0o600)
	world.WriteMackup(".zlast", "untouched\n", 0o600)
	world.WriteFile(".mdir", "a file where the folder belongs\n", 0o600)

	result := world.Run("link", probeKey).ExpectFailureExit()

	if stderr := result.StderrText(); !strings.Contains(stderr, world.Path(".mdir", "inner")) {
		t.Errorf("link stderr = %q, want a diagnostic naming %s", result.Stderr, world.Path(".mdir", "inner"))
	}
	if strings.Contains(result.StderrText(), "file(s) could not be copied") {
		t.Errorf("link stderr = %q, want no end-of-run summary: appspec/01 section 5 gives link commands no aggregation path", result.Stderr)
	}
	// Earlier files transitioned; later files untouched.
	expectLinkedIntoUnchangedStorage(t, world.Path(".afirst"), world.Mackup(".afirst"), "transitioned\n")
	expectAbsent(t, world.Path(".zlast"))
	expectContent(t, world.Mackup(".zlast"), "untouched\n")
}

func TestAHomePathThatCannotBeInspectedStopsBeforeTheLinkRatherThanInsideIt(t *testing.T) {
	// appspec/06 splits this command's steps 3 and 4 on whether "a file/dir/link
	// already exists at the home path". A stat of that path which fails for a
	// reason OTHER than its absence has answered neither question, so the
	// procedure may not take either branch -- and the branch it would fall into
	// is step 4, which creates the link with no prompt at all.
	//
	// The hazard is weaker than link install's version of this guard, and
	// saying so is the honest way to keep it: there the fall-through copies
	// over the uninspectable path and then deletes the home file, where here
	// os.Symlink refuses an occupied path outright, so no constructible fixture
	// loses data. What the guard buys is that the program does not assert
	// "nothing is there" on the strength of a stat that established nothing.
	//
	// Which means the fixture is the same one the case above uses and the two
	// spellings are close: both exit non-zero, and both name the home path,
	// since a run that reached syncfs.Link fails at the same ENOTDIR one call
	// later. What tells them apart is WHICH paths the diagnostic names -- the
	// guard has only looked at the home side, so it names the home path ALONE,
	// where a failure inside the link names both ends of the link it attempted.
	// That is the observable form of "no prompt and no write was attempted".
	world := newSyncWorld(t, ".mdir/inner")
	world.WriteMackup(".mdir/inner", "shared\n", 0o600)
	world.WriteFile(".mdir", "a file where the folder belongs\n", 0o600)

	result := world.Run("link", probeKey).ExpectFailureExit()

	stderr := result.StderrText()
	if !strings.Contains(stderr, world.Path(".mdir", "inner")) {
		t.Errorf("link stderr = %q, want a diagnostic naming the home path %s it could not inspect", result.Stderr, world.Path(".mdir", "inner"))
	}
	if strings.Contains(stderr, world.Mackup(".mdir", "inner")) {
		t.Errorf("link stderr = %q, want it to name the home path ALONE: naming %s too means the run reached the link instead of stopping at the inspection", result.Stderr, world.Mackup(".mdir", "inner"))
	}
	// Nothing was attempted, on either side.
	expectContent(t, world.Path(".mdir"), "a file where the folder belongs\n")
	expectContent(t, world.Mackup(".mdir", "inner"), "shared\n")
}

func TestEndOfInputAtTheLinkPromptEndsTheRunUnguarded(t *testing.T) {
	// appspec/07: a prompt reached with no force flag and stdin at end of input
	// "cannot obtain a valid answer and terminates with a nonzero exit (an
	// unhandled end-of-input condition -- the unguarded regime)". Not an
	// implicit no: the file is left exactly as it was, and the run does not
	// carry on to the next file.
	world := newSyncWorld(t, ".conflict", ".zfresh")
	world.WriteFile(".conflict", "the user's own\n", 0o600)
	world.WriteMackup(".conflict", "shared\n", 0o600)
	world.WriteMackup(".zfresh", "not reached\n", 0o600)

	world.Run("link", probeKey).ExpectFailureExit()

	expectRealFile(t, world.Path(".conflict"))
	expectContent(t, world.Path(".conflict"), "the user's own\n")
	expectAbsent(t, world.Path(".zfresh"))
}

func TestLinkAndLinkInstallAgreeOnWhatAlreadyLinkedMeans(t *testing.T) {
	// appspec/01 section 2: the already-linked predicate "is used identically
	// by four operations", and link install's guard and `link`'s guard are two
	// of them. Running one command after the other is the only way to observe
	// that they agree, and disagreement is the failure appspec/01 says a
	// four-times-coded check produces.
	//
	// It is also the two entry doors' convergence, which appspec/00 implies and
	// no single command states: whichever door a machine came in by, the file
	// ends in the same state and the other door then does nothing.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", "home content\n", 0o644)

	world.Run("link", "install", probeKey).ExpectExit(0).ExpectSilentStderr()
	before := world.Snapshot()

	result := world.Run("--verbose", "link", probeKey).ExpectExit(0).ExpectSilentStderr()

	if !strings.Contains(result.StdoutText(), doingNothingLinked) {
		t.Errorf("link after link install stdout = %q, want the already-linked trace", result.Stdout)
	}
	world.ExpectUnchanged(before)
}

// -- appspec/06 step 1's platform condition, and its fixture -----------------

// libraryFile is a home-relative path under ~/Library/: the one path shape
// appspec/00 "Platform assumptions" makes behave differently on the two systems
// this program targets, and only under the plain `link` command.
//
// A .plist under Library/Preferences is what such a path really is -- the
// shipped catalog is full of them -- rather than a name invented to match the
// prefix.
const libraryFile = "Library/Preferences/Probe.plist"

// libraryControl is the file the platform cases check alongside libraryFile,
// and its name is chosen so that it sorts AFTER libraryFile rather than before.
//
// That is the whole reason it is not spelled ".zprobrc" like the control file
// in every other case here: files are visited in sorted order (appspec/01
// section 1) and "." precedes "L", so a dotted name would be linked BEFORE the
// Library path and would say nothing about whether the run carried on past the
// skip. Lowercase "z" follows "L", so this one does.
const libraryControl = "zprobrc"

// newLibraryWorld seeds storage with libraryFile and the control file sorted
// after it, for the two platform cases that observe opposite outcomes on the
// same arrangement.
//
// The control file is what makes each of those cases able to fail for the right
// reason. On Linux the claim is that ONE file was skipped and the run carried
// ON PAST IT -- a program that stopped, or that skipped everything, satisfies
// an assertion about the Library path alone -- and on macOS it is that the same
// arrangement links both.
func newLibraryWorld(t *testing.T) *syncWorld {
	t.Helper()
	world := newSyncWorld(t, libraryFile, libraryControl)
	world.WriteMackup(libraryFile, "a macOS preference\n", 0o600)
	world.WriteMackup(libraryControl, "portable\n", 0o600)
	return world
}
