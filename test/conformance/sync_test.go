//go:build conformance

// The copy half of the suite: `backup` and `restore` as
// appspec/06-sync-operations.md specifies them -- ONE procedure parameterized
// by a direction record -- together with the system-wide machinery of
// appspec/01-architecture.md they are the first commands to exercise: the
// two-level sorted fan-out, single-application scoping, the Mackup-folder gate,
// the one confirmation mechanism, dry-run and verbose, and the partial-failure
// contract.
//
// These are the first commands whose EFFECT is observable. Everything before
// them either refused or printed: the enumeration cases watch stdout, and the
// config and database cases watch a program decline to start. Here the
// assertion is what is on disk afterwards, which is why so many cases below end
// in a Snapshot comparison rather than in a string match -- appspec/00 promise 3
// (idempotency), promise 7 (home-relative layout) and promise 9 (a partial run
// never exits 0) are all claims about the filesystem and the exit code, not
// about wording.
//
// Two habits are deliberate and worth keeping. Cases sync a PROBE application
// whose file set they wrote themselves, rather than `vim`, so that the fixture
// and the assertion cannot drift apart when the shipped catalog changes -- the
// catalog is pinned by its own cases elsewhere. And a case that means "the
// program did the thing" asserts the thing on disk, never merely the progress
// line: a progress line is printed before the copy, so a program that printed
// and then did nothing satisfies every output-only assertion in this file.

package conformance

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The literal shapes appspec/06 and appspec/07 give the copy operation. Spelled
// once here so a case says what it means and a rewording is one edit.
const (
	backupVerb  = "Backing up"
	restoreVerb = "Recovering"

	// createFolderFirstLine and createFolderSecondLine are the two halves of
	// the folder-creation prompt of appspec/06, which appspec/07 also lists
	// among the prompts that exist.
	createFolderFirstLine  = "Mackup needs a directory to store your configuration files"
	createFolderSecondLine = "Do you want to create it now?"

	// noHomeRefusal and missingFolderError are the declined-folder and
	// missing-folder rows of appspec/07's error table.
	noHomeRefusal      = "Error: Mackup can't do anything without a home =("
	missingFolderError = "Error: Unable to find the Mackup folder: "

	// answerHint is what appspec/07 puts after every question: "the question
	// text followed by ` <Yes|No> `".
	answerHint = " <Yes|No> "

	// forceHint is the hint appspec/06 gives backup's replace prompt and
	// withholds from restore's.
	forceHint = "(use --force to skip this prompt)"

	// copyFailurePrefix opens the per-file line of appspec/06's
	// partial-failure contract; backupIncomplete and restoreIncomplete open
	// the end-of-run summary in each direction.
	copyFailurePrefix = "Error: Unable to copy "
	backupIncomplete  = "Backup incomplete: "
	restoreIncomplete = "Restore incomplete: "
)

// probeKey is the application key the cases in this file sync. It is a
// definition the case drops into ~/.mackup, so the file set under test is the
// case's own rather than whatever the shipped catalog happens to name.
const probeKey = "probe"

// A syncWorld is a World ready for a sync command: a resolvable storage root,
// the Mackup folder created so the fifth gate is not the thing under test, and
// a probe application whose file set the case chose.
//
// The Mackup folder is created up front on purpose. appspec/01 section 4 makes
// it a per-command gate, and a case about the per-file procedure that had to
// answer the folder prompt first would be asserting two contracts at once and
// would report a gate failure as a copy failure. The cases that ARE about the
// gate build their worlds without this.
type syncWorld struct {
	*World
	folder string
}

func newSyncWorld(t *testing.T, files ...string) *syncWorld {
	t.Helper()
	world := NewWorld(t)
	folder := world.UseMackupFolder()
	world.WriteFile(".mackup/"+probeKey+".cfg", probeDefinition(files...), 0o600)
	return &syncWorld{World: world, folder: folder}
}

// probeDefinition is a definition file naming the given home-relative paths.
func probeDefinition(files ...string) string {
	definition := "[application]\nname = Probe\n"
	if len(files) > 0 {
		definition += "\n[configuration_files]\n" + strings.Join(files, "\n") + "\n"
	}
	return definition
}

// Mackup resolves a path inside the Mackup folder, the way World.Path resolves
// one inside home.
func (w *syncWorld) Mackup(relative ...string) string {
	return filepath.Join(append([]string{w.folder}, relative...)...)
}

// WriteMackup creates a file inside the Mackup folder, making its parents. The
// storage-side mirror of World.WriteFile, for the cases that need a
// destination copy to already exist.
func (w *syncWorld) WriteMackup(relative, content string, perm fs.FileMode) string {
	w.t.Helper()
	path := w.Mackup(relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		w.t.Fatalf("creating the parent of %s in the Mackup folder: %v", relative, err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		w.t.Fatalf("writing %s in the Mackup folder: %v", relative, err)
	}
	return path
}

// readFile reads a file the program was supposed to have written, failing the
// case if it is not there.
func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(content)
}

// expectContent asserts a path holds exactly want.
func expectContent(t *testing.T, path, want string) {
	t.Helper()
	if got := readFile(t, path); got != want {
		t.Errorf("%s holds %q, want %q", path, got, want)
	}
}

// expectMode asserts a path's permission bits, which appspec/06 fixes at
// 0600 for files and 0700 for directories on every write.
func expectMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s has mode %04o, want %04o", path, got, want)
	}
}

// expectAbsent asserts nothing is at a path.
func expectAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("%s exists, want nothing there", path)
	}
}

// expectRealFile asserts a path is a regular file and not a symlink --
// appspec/06's "restore never creates symlinks" and backup's "the home files
// are left in place as real files", which a content check alone does not make.
func expectRealFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("%s is %s, want a regular file", path, info.Mode().Type())
	}
}

// symlink creates a symbolic link, failing the case if it cannot.
func symlink(t *testing.T, target, linkPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o700); err != nil {
		t.Fatalf("creating the parent of %s: %v", linkPath, err)
	}
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatalf("linking %s -> %s: %v", linkPath, target, err)
	}
}

// -- The net effects of appspec/06 ------------------------------------------

func TestBackupCopiesHomeFilesIntoTheMackupFolderAndLeavesHomeAlone(t *testing.T) {
	// appspec/06 "Net effects": "for each home config file/dir, an identical
	// copy exists at the same relative path under the Mackup folder, with
	// 0600/0700 permissions. The home files are left in place as real files --
	// backup copies, it does not symlink or move."
	//
	// The last clause is the one worth asserting explicitly. A backup that
	// moved the file and linked it back would satisfy "an identical copy
	// exists under the Mackup folder" perfectly, and would be `link install`
	// wearing backup's name.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", "set number\n", 0o644)

	world.Run("backup", probeKey).ExpectExit(0).ExpectSilentStderr()

	expectContent(t, world.Mackup(".probrc"), "set number\n")
	expectContent(t, world.Path(".probrc"), "set number\n")
	expectRealFile(t, world.Path(".probrc"))
	expectMode(t, world.Mackup(".probrc"), 0o600)
}

func TestRestoreOverwritesHomeWithTheMackupCopyAndNeverCreatesASymlink(t *testing.T) {
	// appspec/06 "Net effects": "home files are overwritten with the Mackup
	// copies (after confirmation when a home copy exists), copied as real
	// files with 0600/0700 permissions. Restore never creates symlinks."
	world := newSyncWorld(t, ".probrc")
	world.WriteMackup(".probrc", "from storage\n", 0o600)

	world.Run("restore", probeKey).ExpectExit(0).ExpectSilentStderr()

	expectContent(t, world.Path(".probrc"), "from storage\n")
	expectRealFile(t, world.Path(".probrc"))
	expectMode(t, world.Path(".probrc"), 0o600)
	// The storage copy is untouched: appspec/01 section 2, "no transition ever
	// deletes the storage copy".
	expectContent(t, world.Mackup(".probrc"), "from storage\n")
}

func TestASecondIdenticalRunDoesNothingAndPromptsForNothing(t *testing.T) {
	// The done-claim of this ticket and appspec/00 promise 3, for BOTH
	// directions: "a second identical run detects content-identical
	// destinations and skips them, doing nothing and prompting for nothing."
	//
	// Run with stdin at end-of-input, which is what makes "prompts for
	// nothing" checkable rather than asserted: appspec/07 makes a prompt
	// reached with no answer available a nonzero exit, so a second run that
	// asked anything would fail here instead of quietly being answered.
	for _, command := range []string{"backup", "restore"} {
		world := newSyncWorld(t, ".probrc")
		world.WriteFile(".probrc", "set number\n", 0o644)
		world.WriteMackup(".probrc", "set number\n", 0o600)

		world.Run(command, probeKey).ExpectExit(0)

		before := world.Snapshot()
		second := world.Run(command, probeKey).ExpectExit(0).ExpectSilentStderr()
		if second.StdoutText() != "" {
			t.Errorf("a second %s printed %q, want nothing: an identical destination is skipped",
				command, second.Stdout)
		}
		world.ExpectUnchanged(before)
	}
}

func TestApplicationsAndFilesAreProcessedInSortedOrder(t *testing.T) {
	// appspec/01 section 1: "applications in sorted (ascending,
	// byte/lexicographic) key order; files within each application in sorted
	// path order". A whole-program guarantee, and the only place it is
	// observable is the order the progress lines come out in.
	//
	// Two applications and three files each, named so that the sorted order is
	// not the order they are written in -- a program preserving definition
	// order, or map order, produces a different sequence.
	world := NewWorld(t)
	world.UseMackupFolder()
	world.WriteFile(".mackup/zulu.cfg", probeDefinition(".z-second", ".z-first"), 0o600)
	world.WriteFile(".mackup/alpha.cfg", probeDefinition(".a-second", ".a-first"), 0o600)
	for _, name := range []string{".z-second", ".z-first", ".a-second", ".a-first"} {
		world.WriteFile(name, name+"\n", 0o600)
	}

	// Three applications end up with files here, not two: the shipped `mackup`
	// definition names ~/.mackup and ~/.mackup.cfg, and this world has both --
	// the definition directory the two probes were dropped into, and the
	// config. That is not noise to be filtered out, it is the case's evidence
	// for the OUTER loop: `mackup` sorts between `alpha` and `zulu`, so its
	// files appear between theirs and not beside the definitions that created
	// them. Every other key in the catalog has no file in this home, so its
	// per-file procedure skips silently and prints nothing.
	result := world.Run("backup").ExpectExit(0).ExpectSilentStderr()

	want := []string{
		backupVerb + " .a-first ...",
		backupVerb + " .a-second ...",
		backupVerb + " .mackup ...",
		backupVerb + " .mackup.cfg ...",
		backupVerb + " .z-first ...",
		backupVerb + " .z-second ...",
	}
	got := strings.Split(strings.TrimSuffix(result.StdoutText(), "\n"), "\n")
	if len(got) != len(want) {
		t.Fatalf("backup printed %q, want exactly the six progress lines %q", result.Stdout, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("progress line %d = %q, want %q: applications and files are both in sorted order",
				i, got[i], want[i])
		}
	}
}

// -- Permissions: appspec/06 "Permissions (clamped on every write)" ----------

func TestACopiedTreeIsClampedTo0700DirectoriesAnd0600FilesRecursively(t *testing.T) {
	// "Whenever a file/folder is copied ... its mode is set recursively:
	// regular files -> 0600, directories -> 0700."
	//
	// Recursively is the word under test. A clamp applied only to the path
	// named -- the copy's root -- leaves every file inside a config DIRECTORY
	// at whatever mode it had, which for a ~/.ssh or ~/.gnupg tree is the
	// difference the clamp exists to make.
	world := newSyncWorld(t, ".probdir")
	world.WriteFile(".probdir/inner/leaf", "leaf\n", 0o644)
	world.WriteFile(".probdir/top", "top\n", 0o666)

	world.Run("backup", probeKey).ExpectExit(0).ExpectSilentStderr()

	expectMode(t, world.Mackup(".probdir"), 0o700)
	expectMode(t, world.Mackup(".probdir", "inner"), 0o700)
	expectMode(t, world.Mackup(".probdir", "inner", "leaf"), 0o600)
	expectMode(t, world.Mackup(".probdir", "top"), 0o600)
}

func TestTheDirectoriesCreatedOnTheWayToADestinationAreNotClamped(t *testing.T) {
	// The other half of the clamp rule, and the half a reader is most likely
	// to get wrong. appspec/06 makes the clamp a post-condition of the path
	// that was COPIED; the ancestors created to reach that path were not
	// copied and are not clamped. Narrowing them would make backup change the
	// permissions of directories inside the user's storage that it was never
	// asked to manage.
	//
	// The expected mode is taken from a directory this case creates the same
	// way rather than written as a number, because the answer depends on the
	// process umask and a hardcoded 0755 would fail on a machine with a
	// different one -- or, worse, would PASS on a machine with umask 077,
	// where the clamped and unclamped answers coincide and the case observes
	// nothing.
	world := newSyncWorld(t, ".config/probe/probrc")
	world.WriteFile(".config/probe/probrc", "nested\n", 0o600)

	probe := filepath.Join(world.Root, "umask-probe", "a", "b")
	if err := os.MkdirAll(probe, 0o777); err != nil {
		t.Fatalf("creating the umask probe: %v", err)
	}
	info, err := os.Lstat(probe)
	if err != nil {
		t.Fatalf("stat the umask probe: %v", err)
	}
	unclamped := info.Mode().Perm()
	if unclamped == 0o700 {
		t.Skipf("this process's umask makes an unclamped directory 0700, which is also the clamped mode, so this case cannot tell them apart")
	}

	world.Run("backup", probeKey).ExpectExit(0).ExpectSilentStderr()

	expectMode(t, world.Mackup(".config"), unclamped)
	expectMode(t, world.Mackup(".config", "probe"), unclamped)
	// The file itself IS the copied path, so it is clamped.
	expectMode(t, world.Mackup(".config", "probe", "probrc"), 0o600)
}

// -- The copy primitive's semantics, seen through backup ---------------------

func TestReplacingADirectoryDestinationDeletesItBeforeCopying(t *testing.T) {
	// appspec/06 step 3, on a yes: "delete the destination, then copy
	// source->destination". For a directory that is the whole destination, so
	// an entry only the destination had is gone afterwards.
	//
	// This is worth a case because the copy primitive MERGES -- appspec/06's
	// copy(src, dst) leaves destination-only files in place -- and a reader
	// who knows that will expect a merge here. The merge is not reachable
	// through this step: the delete happens first, so the destination the copy
	// merges into is one that was just removed. What the user gets instead of
	// a silent loss is the directory detail printed above the prompt, which
	// names the entry that is about to go ("only in target: ...") before
	// asking.
	world := newSyncWorld(t, ".probdir")
	world.WriteFile(".probdir/shared", "from home\n", 0o600)
	world.WriteMackup(".probdir/shared", "stale\n", 0o600)
	world.WriteMackup(".probdir/from-another-machine", "gone after the replace\n", 0o600)

	result := world.RunWithInput("yes\n", "backup", probeKey).ExpectExit(0).ExpectSilentStderr()

	if !strings.Contains(result.StdoutText(), "only in target: from-another-machine") {
		t.Errorf("backup printed %q, want the directory detail to name the destination-only entry before the prompt", result.Stdout)
	}
	expectContent(t, world.Mackup(".probdir", "shared"), "from home\n")
	expectAbsent(t, world.Mackup(".probdir", "from-another-machine"))
}

func TestASymlinkedSourceIsCopiedAsTheRealFileItPointsAt(t *testing.T) {
	// appspec/06's per-file step 1 admits a source that "exists as a regular
	// file or directory", and the copy primitive follows symlinks. So a home
	// path that is a symlink to a real file elsewhere in home is backed up as
	// that file's CONTENT, written into storage as a real file -- which is
	// what makes the storage folder portable to a machine where the link's
	// target does not exist.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile("elsewhere/real", "real content\n", 0o600)
	symlink(t, world.Path("elsewhere", "real"), world.Path(".probrc"))

	world.Run("backup", probeKey).ExpectExit(0).ExpectSilentStderr()

	expectContent(t, world.Mackup(".probrc"), "real content\n")
	expectRealFile(t, world.Mackup(".probrc"))
}

func TestASymlinkedDirectoryInsideTheSourceTreeIsCopiedAsRealContent(t *testing.T) {
	// The same rule one level down: the recursive copy classifies each entry
	// with a following stat, so a directory symlink inside a config directory
	// is descended into and its contents written as real files. Reproducing
	// the link inside storage would leave a second machine pointing at a path
	// that exists only on the first.
	world := newSyncWorld(t, ".probdir")
	world.WriteFile("outside/leaf", "leaf\n", 0o600)
	world.WriteFile(".probdir/plain", "plain\n", 0o600)
	symlink(t, world.Path("outside"), world.Path(".probdir", "linked"))

	world.Run("backup", probeKey).ExpectExit(0).ExpectSilentStderr()

	expectContent(t, world.Mackup(".probdir", "linked", "leaf"), "leaf\n")
	expectRealFile(t, world.Mackup(".probdir", "linked", "leaf"))
}

// -- The link-skip: appspec/06 step 2, backup only ---------------------------

func TestBackupSkipsAHomeFileAlreadyLinkedIntoTheMackupFolder(t *testing.T) {
	// appspec/06 step 2, the one genuine behavioural asymmetry between the two
	// directions: "if the source is already a symlink to its mackup path
	// (already backed up via link install), skip it ... nothing is copied."
	//
	// Nothing is copied is the assertion, and the fixture is what makes it
	// mean something: without the skip the copy would follow the home link and
	// write the storage file's own contents back over itself, which succeeds
	// and looks identical. The storage copy's mtime is what tells them apart,
	// and Snapshot records it.
	world := newSyncWorld(t, ".probrc")
	world.WriteMackup(".probrc", "the real file\n", 0o600)
	symlink(t, world.Mackup(".probrc"), world.Path(".probrc"))

	before := world.Snapshot()
	result := world.Run("backup", probeKey).ExpectExit(0).ExpectSilentStderr()
	if result.StdoutText() != "" {
		t.Errorf("backup printed %q, want nothing: an already-linked file is skipped", result.Stdout)
	}
	world.ExpectUnchanged(before)
}

func TestVerboseSaysWhyAnAlreadyLinkedFileWasSkipped(t *testing.T) {
	// appspec/06 step 2: "verbose prints a 'Skipping ... already linked to
	// ...' trace". appspec/01 section 3 makes verbose observationally pure, so
	// the trace is the whole of what the flag adds here.
	world := newSyncWorld(t, ".probrc")
	world.WriteMackup(".probrc", "the real file\n", 0o600)
	symlink(t, world.Mackup(".probrc"), world.Path(".probrc"))

	before := world.Snapshot()
	result := world.Run("--verbose", "backup", probeKey).ExpectExit(0).ExpectSilentStderr()
	if !strings.Contains(result.StdoutText(), "Skipping") ||
		!strings.Contains(result.StdoutText(), "already linked to") {
		t.Errorf("verbose backup printed %q, want a \"Skipping ... already linked to ...\" trace", result.Stdout)
	}
	world.ExpectUnchanged(before)
}

func TestAHomeSymlinkPointingSomewhereElseIsBackedUpAsItsContents(t *testing.T) {
	// The predicate is "the home path is a symlink to ITS MACKUP PATH", not
	// "the home path is a symlink". appspec/01 section 2 makes the answer turn
	// on the two resolving to the same file, and a predicate that accepted any
	// live home symlink would skip -- leaving a stale storage copy while
	// reporting a clean backup, which is the failure mode the user would never
	// see until they restored.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile("elsewhere/real", "the user's own file\n", 0o600)
	world.WriteMackup(".probrc", "stale storage copy\n", 0o600)
	symlink(t, world.Path("elsewhere", "real"), world.Path(".probrc"))

	world.RunWithInput("yes\n", "backup", probeKey).ExpectExit(0).ExpectSilentStderr()

	expectContent(t, world.Mackup(".probrc"), "the user's own file\n")
}

func TestTheLinkSkipHoldsWhenTheStorageRootIsReachedThroughASymlink(t *testing.T) {
	// appspec/01 section 2 words the last condition of the predicate as "the
	// two resolve to the same file", and identity is what that means. The
	// arrangement that tells identity apart from a string comparison of link
	// targets is the ordinary one for this program: a storage root reached
	// through a symlink, so the home link's target is spelled differently from
	// the mackup path the program computed.
	//
	// Without identity the file is not recognized as linked, and backup copies
	// the storage file over itself through its own home link. Snapshot's mtime
	// is what sees that.
	world := NewWorld(t)
	// The config names `storage`, and `storage` is a link to the directory
	// that really holds the Mackup folder.
	world.WriteFile(".mackup.cfg", "[storage]\nengine = file_system\npath = storage\n", 0o600)
	real := world.Path("volume")
	if err := os.MkdirAll(filepath.Join(real, "Mackup"), 0o700); err != nil {
		t.Fatalf("creating the real storage tree: %v", err)
	}
	symlink(t, real, world.Path("storage"))
	world.WriteFile(".mackup/"+probeKey+".cfg", probeDefinition(".probrc"), 0o600)

	// The home link points at the path THROUGH the symlinked root, which is
	// not the path the program joins from the config.
	if err := os.WriteFile(filepath.Join(real, "Mackup", ".probrc"), []byte("real\n"), 0o600); err != nil {
		t.Fatalf("writing the storage copy: %v", err)
	}
	symlink(t, world.Path("storage", "Mackup", ".probrc"), world.Path(".probrc"))

	before := world.Snapshot()
	result := world.Run("backup", probeKey).ExpectExit(0).ExpectSilentStderr()
	if result.StdoutText() != "" {
		t.Errorf("backup printed %q, want nothing: the file is linked, however the root was reached", result.Stdout)
	}
	world.ExpectUnchanged(before)
}

func TestRestoreHasNoLinkSkipAndReplacesTheLinkWithARealFile(t *testing.T) {
	// The asymmetry, from the other side. appspec/01 section 1: backup skips a
	// home copy that is already a symlink into storage; "restore has no such
	// skip because the storage copy is always treated as the real file", and
	// appspec/06 adds that restore "never creates symlinks".
	//
	// So the same fixture that makes backup do nothing makes restore replace
	// the link with a real file -- after a prompt, because something exists at
	// the destination.
	world := newSyncWorld(t, ".probrc")
	world.WriteMackup(".probrc", "the real file\n", 0o600)
	symlink(t, world.Mackup(".probrc"), world.Path(".probrc"))

	world.RunWithInput("yes\n", "restore", probeKey).ExpectExit(0).ExpectSilentStderr()

	expectRealFile(t, world.Path(".probrc"))
	expectContent(t, world.Path(".probrc"), "the real file\n")
	// Storage is never emptied: appspec/01 section 2.
	expectContent(t, world.Mackup(".probrc"), "the real file\n")
}

// -- Step 1: a source that is not there --------------------------------------

func TestAFileThatIsNotInHomeIsSkippedSilently(t *testing.T) {
	// appspec/06 step 1: "if the source path does not exist as a regular file
	// or directory, skip it silently."
	//
	// Silently is the assertion. The shipped catalog names thousands of paths
	// no machine has, so a message here would bury the run's actual work; and
	// exit 0 is the other half -- an absent config file is the ordinary case,
	// not a failure.
	world := newSyncWorld(t, ".probrc", ".present")
	world.WriteFile(".present", "here\n", 0o600)

	result := world.Run("backup", probeKey).ExpectExit(0).ExpectSilentStderr()

	if strings.Contains(result.StdoutText(), ".probrc") {
		t.Errorf("backup printed %q, want no mention of the absent .probrc", result.Stdout)
	}
	expectAbsent(t, world.Mackup(".probrc"))
	expectContent(t, world.Mackup(".present"), "here\n")
}

func TestADanglingHomeSymlinkIsSkippedRatherThanFailing(t *testing.T) {
	// appspec/01 section 2: a dangling home symlink "reads as false -- never
	// an error" for the predicate, and step 1's existence test follows the
	// link, so there is nothing to copy. Both readings agree on the outcome
	// and neither of them raises, which is the point: a broken link in a home
	// directory is ordinary residue, not a reason to fail a backup.
	world := newSyncWorld(t, ".probrc")
	symlink(t, world.Path("nowhere"), world.Path(".probrc"))

	before := world.Snapshot()
	result := world.Run("backup", probeKey).ExpectExit(0).ExpectSilentStderr()
	if result.StdoutText() != "" {
		t.Errorf("backup printed %q, want nothing for a dangling link", result.Stdout)
	}
	world.ExpectUnchanged(before)
}

// -- Step 3: drift detection, the header, and the diff ----------------------

func TestTheDriftHeaderAndDiffAreOnStdoutAheadOfThePrompt(t *testing.T) {
	// appspec/06's stream note, which it flags as a correction: "the 'differs
	// between ...' header and the diff detail are printed to stdout, not
	// stderr. Only genuine copy-failure lines and the end-of-run 'incomplete'
	// summary go to stderr." appspec/07 repeats it under "Do not generalize
	// warnings -> stderr" and names this message.
	//
	// This is the message a reimplementation is most likely to misroute, which
	// is why the case asserts the stream and not merely the text: a program
	// that printed the identical header on stderr would satisfy a Contains on
	// the combined output.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", "new\n", 0o600)
	world.WriteMackup(".probrc", "old\n", 0o600)

	result := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0).ExpectSilentStderr()

	stdout := result.StdoutText()
	header := ".probrc differs between home and Mackup:"
	if !strings.Contains(stdout, header) {
		t.Fatalf("backup stdout = %q, want the header %q", result.Stdout, header)
	}
	if !strings.Contains(stdout, "-old") || !strings.Contains(stdout, "+new") {
		t.Errorf("backup stdout = %q, want the unified diff of the two files under the header", result.Stdout)
	}
	// Order is contract too: appspec/06 has the header, then the diff, then
	// the prompt. A prompt shown before the reason for it is a prompt the user
	// answers blind.
	prompt := strings.Index(stdout, "Are you sure that you want to replace it?")
	if prompt < 0 || prompt < strings.Index(stdout, header) {
		t.Errorf("backup stdout = %q, want the header and diff BEFORE the prompt", result.Stdout)
	}
}

func TestTheDriftPhrasingFollowsTheDirectionRecord(t *testing.T) {
	// appspec/06's direction table gives backup the phrasing "home and Mackup"
	// and restore "Mackup and home". One of the six columns that make the two
	// commands one operation, and the only one visible in this header.
	for _, test := range []struct {
		command  string
		phrasing string
	}{
		{"backup", "home and Mackup"},
		{"restore", "Mackup and home"},
	} {
		world := newSyncWorld(t, ".probrc")
		world.WriteFile(".probrc", "home side\n", 0o600)
		world.WriteMackup(".probrc", "storage side\n", 0o600)

		result := world.RunWithInput("no\n", test.command, probeKey).ExpectExit(0)
		want := ".probrc differs between " + test.phrasing + ":"
		if !strings.Contains(result.StdoutText(), want) {
			t.Errorf("%s stdout = %q, want the header %q", test.command, result.Stdout, want)
		}
	}
}

func TestATypeMismatchIsOneLineSayingWhichWayRound(t *testing.T) {
	// appspec/06 "Drift detection": "if one is a directory and the other a file
	// (type mismatch): differing, with a one-line 'type mismatch: folder vs
	// file' (or 'file vs folder') detail."
	//
	// Both orders, because the detail is written source-first and a
	// reimplementation that printed one message for both cases would pass a
	// single-direction case while telling half its users the wrong thing.
	for _, test := range []struct {
		command string
		detail  string
	}{
		// backup: the source is home, which holds the directory.
		{"backup", "type mismatch: folder vs file"},
		// restore: the source is storage, which holds the file.
		{"restore", "type mismatch: file vs folder"},
	} {
		world := newSyncWorld(t, ".probthing")
		world.WriteFile(".probthing/inside", "a directory in home\n", 0o600)
		world.WriteMackup(".probthing", "a file in storage\n", 0o600)

		result := world.RunWithInput("no\n", test.command, probeKey).ExpectExit(0)
		if !strings.Contains(result.StdoutText(), test.detail) {
			t.Errorf("%s stdout = %q, want the detail %q", test.command, result.Stdout, test.detail)
		}
	}
}

func TestADirectoryComparisonIsRecursiveAndNamesTheThreeGroups(t *testing.T) {
	// appspec/06: two directories are "compared recursively by content (not
	// shallow stat). Identical only if every file matches byte-for-byte and
	// neither side has extra entries. Otherwise the detail lists, sorted:
	// changed files ('changed: <name>'), files present only in source ('only
	// in source: <name>'), and files present only in destination ('only in
	// target: <name>')."
	//
	// One fixture producing all three groups at once, because the claim is
	// about the list and a case per group could pass over an implementation
	// that emitted only the group it was asked about.
	world := newSyncWorld(t, ".probdir")
	world.WriteFile(".probdir/changed", "home version\n", 0o600)
	world.WriteFile(".probdir/home-only", "only here\n", 0o600)
	world.WriteFile(".probdir/same", "identical\n", 0o600)
	world.WriteMackup(".probdir/changed", "storage version\n", 0o600)
	world.WriteMackup(".probdir/storage-only", "only there\n", 0o600)
	world.WriteMackup(".probdir/same", "identical\n", 0o600)

	result := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0)

	stdout := result.StdoutText()
	for _, want := range []string{
		"changed: changed",
		"only in source: home-only",
		"only in target: storage-only",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("backup stdout = %q, want the detail line %q", result.Stdout, want)
		}
	}
	if strings.Contains(stdout, "same") {
		t.Errorf("backup stdout = %q, want no mention of the entry that matches on both sides", result.Stdout)
	}
}

func TestTwoIdenticalDirectoriesAreTheIdempotencyFixedPointToo(t *testing.T) {
	// The other half of the recursive comparison: a directory whose every file
	// matches is IDENTICAL, so the file is skipped with no prompt. A shallow
	// stat comparison would report two trees with the same size and mtime as
	// differing (or as identical when they are not), and the whole
	// idempotency promise of appspec/00 rests on this answer.
	world := newSyncWorld(t, ".probdir")
	world.WriteFile(".probdir/one", "one\n", 0o600)
	world.WriteFile(".probdir/two", "two\n", 0o600)
	world.WriteMackup(".probdir/one", "one\n", 0o600)
	world.WriteMackup(".probdir/two", "two\n", 0o600)

	before := world.Snapshot()
	result := world.Run("backup", probeKey).ExpectExit(0).ExpectSilentStderr()
	if result.StdoutText() != "" {
		t.Errorf("backup printed %q, want nothing: the two trees hold the same content", result.Stdout)
	}
	world.ExpectUnchanged(before)
}

func TestADirectoryOnOneSideIsNamedOnceAndNotDescendedInto(t *testing.T) {
	// The directory detail names an entry only one side has, and stops there.
	// Listing what is inside it would turn a whole subtree the user has never
	// seen into a wall of output, and appspec/06 asks for "files present only
	// in source", singular per entry.
	world := newSyncWorld(t, ".probdir")
	world.WriteFile(".probdir/keep", "keep\n", 0o600)
	world.WriteFile(".probdir/subtree/buried", "buried\n", 0o600)
	world.WriteMackup(".probdir/keep", "keep\n", 0o600)

	result := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0)

	stdout := result.StdoutText()
	if !strings.Contains(stdout, "only in source: subtree") {
		t.Errorf("backup stdout = %q, want the one-sided directory named once", result.Stdout)
	}
	if strings.Contains(stdout, "buried") {
		t.Errorf("backup stdout = %q, want no descent into the one-sided directory", result.Stdout)
	}
}

func TestBinaryFilesThatDifferSaySoInsteadOfPrintingADiff(t *testing.T) {
	// appspec/06's third arm: "else compared byte-for-byte; identical if
	// equal, else the detail is 'binary contents differ'." Not a diff -- a
	// unified diff of two binaries is unreadable and, on a terminal, is a way
	// to leave escape sequences in the user's scrollback.
	world := newSyncWorld(t, ".probbin")
	world.WriteFile(".probbin", "\x00\x01\x02 home\xff", 0o600)
	world.WriteMackup(".probbin", "\x00\x01\x02 storage\xfe", 0o600)

	result := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0)
	if !strings.Contains(result.StdoutText(), "binary contents differ") {
		t.Errorf("backup stdout = %q, want the binary detail", result.Stdout)
	}
}

func TestASymlinkAtTheDestinationGetsThePlainPromptWithNoDiff(t *testing.T) {
	// appspec/06: "if either path is a symlink: treated as differing, with no
	// diff detail (plain prompt, no diff printed)."
	//
	// The fixture makes the two paths hold identical CONTENT through the link,
	// so a comparison that followed the link would answer identical and skip.
	// That is the difference between asking about the path and asking about
	// what it points at, and it is the whole of this case.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile("elsewhere/real", "same bytes\n", 0o600)
	world.WriteMackup(".probrc", "same bytes\n", 0o600)
	symlink(t, world.Path("elsewhere", "real"), world.Path(".probrc"))

	result := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0)

	stdout := result.StdoutText()
	if !strings.Contains(stdout, "Are you sure that you want to replace it?") {
		t.Fatalf("backup stdout = %q, want the replace prompt: a symlink is differing however its content compares", result.Stdout)
	}
	if strings.Contains(stdout, "differs between") {
		t.Errorf("backup stdout = %q, want the plain prompt with no drift header: a symlink pair has no detail", result.Stdout)
	}
}
