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
	"fmt"
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

// newGateWorld is the world for the cases about the Mackup-folder gate itself:
// resolvable storage and a probe definition, but NO Mackup folder, so the fifth
// gate of appspec/01 section 4 is the thing under test rather than the thing in
// the way. It returns the world and the folder path the gate will decide about.
//
// The mirror image of newSyncWorld, which creates the folder for the same
// reason this one does not.
func newGateWorld(t *testing.T, files ...string) (*World, string) {
	t.Helper()
	world := NewWorld(t)
	root := world.UseResolvableStorage()
	world.WriteFile(".mackup/"+probeKey+".cfg", probeDefinition(files...), 0o600)
	return world, filepath.Join(root, "Mackup")
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
	world.WriteFile(".mackup.cfg", storageSection, 0o600)
	real := world.Path("volume")
	if err := os.MkdirAll(filepath.Join(real, "Mackup"), 0o700); err != nil {
		t.Fatalf("creating the real storage tree: %v", err)
	}
	symlink(t, real, world.Path("storage"))
	world.WriteFile(".mackup/"+probeKey+".cfg", probeDefinition(".probrc"), 0o600)

	// The home link points at the REAL path, which is not the path the program
	// joins from the config -- the config names `storage`, so the program
	// computes <home>/storage/Mackup/.probrc while the link reads
	// <home>/volume/Mackup/.probrc. That difference is the whole fixture, and
	// it was absent until now: the link used to be written through the
	// symlinked root, which spells it EXACTLY as the program spells it, so a
	// predicate comparing link text to the computed path passed this case
	// while failing the property the case is named for. Injection is what
	// found it -- the mutation replacing os.SameFile with a Readlink string
	// comparison survived the whole conformance suite.
	if err := os.WriteFile(filepath.Join(real, "Mackup", ".probrc"), []byte("real\n"), 0o600); err != nil {
		t.Fatalf("writing the storage copy: %v", err)
	}
	symlink(t, filepath.Join(real, "Mackup", ".probrc"), world.Path(".probrc"))

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

// -- Step 3: the property-list arm ------------------------------------------

// The two XML spellings of one property list. They differ in byte length, in
// key order and in whitespace, and mean exactly the same thing -- which is the
// whole point: appspec/06 puts the plist comparison AHEAD of the text
// comparison, so these two files are identical and a program that compared
// them as text would say they differ.
const (
	plistOneWayRound = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>alpha</key>
	<string>one</string>
	<key>beta</key>
	<string>two</string>
</dict>
</plist>
`
	plistTheOtherWayRound = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>beta</key>
  <string>two</string>
  <key>alpha</key>
  <string>one</string>
</dict>
</plist>
`
)

func TestTwoSpellingsOfOnePropertyListAreComparedByContentNotBytes(t *testing.T) {
	// appspec/06 "Drift detection", the first arm of the two-regular-files
	// case: "if both parse as property-list (plist) files: compared by parsed
	// content". A property-list dictionary is unordered, so two files writing
	// the same settings in different key order are the SAME settings, and the
	// idempotency promise of appspec/00 depends on the program saying so --
	// every plist macOS rewrites comes back with its keys in some other order,
	// and a backup that prompted about every one of them each run would be
	// unusable on the platform this program is for.
	//
	// Run with stdin at end-of-input, so "identical" is checkable rather than
	// asserted: a program that reported these two as differing would reach a
	// prompt with no answer available and exit non-zero.
	world := newSyncWorld(t, ".probplist")
	world.WriteFile(".probplist", plistOneWayRound, 0o600)
	world.WriteMackup(".probplist", plistTheOtherWayRound, 0o600)

	before := world.Snapshot()
	result := world.Run("backup", probeKey).ExpectExit(0).ExpectSilentStderr()
	if result.StdoutText() != "" {
		t.Errorf("backup printed %q, want nothing: two spellings of one property list are identical", result.Stdout)
	}
	world.ExpectUnchanged(before)
}

func TestAPropertyListDiffShowsTheStructureAndNotTheMarkup(t *testing.T) {
	// The other half of the same arm: when two property lists do differ, the
	// diff is "a unified diff of their pretty-printed structures" -- not of
	// the files. A diff of the markup would report the XML header, the
	// DOCTYPE and every reordered <key> as changes, which tells the user
	// nothing about the setting that actually moved; and it would be a diff
	// no binary-spelled plist could ever be given at all.
	world := newSyncWorld(t, ".probplist")
	world.WriteFile(".probplist", plistOneWayRound, 0o600)
	world.WriteMackup(".probplist", strings.Replace(plistTheOtherWayRound,
		"<string>one</string>", "<string>changed</string>", 1), 0o600)

	result := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0)

	stdout := result.StdoutText()
	// The rendered structure, which internal/plist writes as one line per
	// value with the dictionary keys quoted and sorted.
	for _, want := range []string{`+  "alpha": "one"`, `-  "alpha": "changed"`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("backup stdout = %q, want the diff line %q from the rendered structure", result.Stdout, want)
		}
	}
	// The diff is source-to-destination and the destination is what changes,
	// so the settings that agree do not appear as changes at all.
	if strings.Contains(stdout, `-  "beta"`) || strings.Contains(stdout, `+  "beta"`) {
		t.Errorf("backup stdout = %q, want the unchanged key out of the diff: the key order is not a difference", result.Stdout)
	}
	for _, markup := range []string{"<string>", "<key>", "DOCTYPE"} {
		if strings.Contains(stdout, markup) {
			t.Errorf("backup stdout = %q, want no %s: the diff is over the parsed structure, not the file", result.Stdout, markup)
		}
	}
}

// -- Step 3: the replace prompt ----------------------------------------------

// replaceQuestion is the invariant half of appspec/07's two replace prompts --
// everything after the type, the destination and the location noun, which the
// direction record supplies.
const replaceQuestion = " already exists in %s. Are you sure that you want to replace it?"

func TestTheReplacePromptNamesTheTypeTheDestinationAndItsLocation(t *testing.T) {
	// appspec/07's two rows, which appspec/06 derives from one prompt and two
	// columns of the direction record: "A <file|folder|link> named <dst>
	// already exists in <destination-location noun>. Are you sure that you
	// want to replace it?", with backup appending "(use --force to skip this
	// prompt)" and restore not.
	//
	// The <type> describes the EXISTING path -- the thing about to be
	// destroyed -- which is why the link row exists and why it is asserted
	// against a destination symlink whose target is an ordinary file: a
	// program reporting what the link points at would say "file" and tell the
	// user the wrong thing about what they are agreeing to lose.
	for _, test := range []struct {
		what     string
		command  string
		build    func(w *syncWorld) string
		typeNoun string
		location string
		hint     string
	}{
		{
			what:    "backup over a file in storage",
			command: "backup",
			build: func(w *syncWorld) string {
				w.WriteFile(".probrc", "home\n", 0o600)
				return w.WriteMackup(".probrc", "storage\n", 0o600)
			},
			typeNoun: "file",
			location: "the Mackup folder",
			hint:     " " + forceHint,
		},
		{
			what:    "restore over a file in home",
			command: "restore",
			build: func(w *syncWorld) string {
				w.WriteMackup(".probrc", "storage\n", 0o600)
				return w.WriteFile(".probrc", "home\n", 0o600)
			},
			typeNoun: "file",
			location: "your home folder",
			hint:     "",
		},
		{
			what:    "backup over a directory in storage",
			command: "backup",
			build: func(w *syncWorld) string {
				w.WriteFile(".probrc", "home\n", 0o600)
				w.WriteMackup(".probrc/inside", "storage\n", 0o600)
				return w.Mackup(".probrc")
			},
			typeNoun: "folder",
			location: "the Mackup folder",
			hint:     " " + forceHint,
		},
		{
			what:    "backup over a symlink in storage",
			command: "backup",
			build: func(w *syncWorld) string {
				w.WriteFile(".probrc", "home\n", 0o600)
				w.WriteFile("elsewhere", "somewhere else\n", 0o600)
				symlink(t, w.Path("elsewhere"), w.Mackup(".probrc"))
				return w.Mackup(".probrc")
			},
			typeNoun: "link",
			location: "the Mackup folder",
			hint:     " " + forceHint,
		},
	} {
		world := newSyncWorld(t, ".probrc")
		destination := test.build(world)

		result := world.RunWithInput("no\n", test.command, probeKey).ExpectExit(0).ExpectSilentStderr()

		want := "A " + test.typeNoun + " named " + destination +
			fmt.Sprintf(replaceQuestion, test.location) + test.hint + answerHint
		if !strings.Contains(result.Stdout, want) {
			t.Errorf("%s: %s stdout = %q, want the prompt %q", test.what, test.command, result.Stdout, want)
		}
	}
}

func TestOnlyBackupsPromptMentionsTheForceFlag(t *testing.T) {
	// The "mentions --force?" column of appspec/06's direction table, asserted
	// as an absence as well as a presence. A prompt is where a user learns
	// what they can do instead of answering, so a hint on the wrong command is
	// a hint about a flag whose effect there is the one they were not offered.
	//
	// The two runs are the same fixture in opposite directions, which is what
	// makes this a claim about the direction record rather than about two
	// separately worded prompts.
	for _, test := range []struct {
		command string
		mention bool
	}{
		{"backup", true},
		{"restore", false},
	} {
		world := newSyncWorld(t, ".probrc")
		world.WriteFile(".probrc", "home\n", 0o600)
		world.WriteMackup(".probrc", "storage\n", 0o600)

		result := world.RunWithInput("no\n", test.command, probeKey).ExpectExit(0)
		if mentions := strings.Contains(result.StdoutText(), forceHint); mentions != test.mention {
			t.Errorf("%s stdout = %q, mentions the --force hint = %v, want %v",
				test.command, result.Stdout, mentions, test.mention)
		}
	}
}

func TestDecliningTheReplacePromptLeavesTheDestinationAloneAndTheRunCarriesOn(t *testing.T) {
	// appspec/06 step 3, on a no: "skip this file". Two claims, and the second
	// is the one a case asserting only the first would miss -- appspec/06's
	// summary calls a declined prompt one of the states re-running converges
	// from, so it cannot end the run, and appspec/00 promise 9 reserves the
	// non-zero exit for a run that could not do what it was asked.
	world := newSyncWorld(t, ".a-probrc", ".b-probrc")
	world.WriteFile(".a-probrc", "home a\n", 0o600)
	world.WriteMackup(".a-probrc", "storage a\n", 0o600)
	world.WriteFile(".b-probrc", "home b\n", 0o600)

	world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0).ExpectSilentStderr()

	expectContent(t, world.Mackup(".a-probrc"), "storage a\n")
	expectContent(t, world.Path(".a-probrc"), "home a\n")
	// The next file in sorted order was still processed.
	expectContent(t, world.Mackup(".b-probrc"), "home b\n")
}

func TestAnUnrecognizedAnswerReAsksTheSameQuestion(t *testing.T) {
	// appspec/07: "any other input re-asks the same question (the loop repeats
	// until a recognized answer is given)". The blank line is deliberately in
	// the input: an empty answer is "any other input", not a default, so a
	// stray return cannot delete a file.
	//
	// The SAME question -- so the count is of the prompt, and the drift header
	// and diff above it are printed once, before the loop. A program that
	// re-printed the diff on every re-ask would produce three copies of it and
	// fail the second assertion.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", "home\n", 0o600)
	world.WriteMackup(".probrc", "storage\n", 0o600)

	result := world.RunWithInput("maybe\n\nyes\n", "backup", probeKey).ExpectExit(0).ExpectSilentStderr()

	stdout := result.StdoutText()
	if got := strings.Count(stdout, "Are you sure that you want to replace it?"); got != 3 {
		t.Errorf("backup asked the question %d times for input \"maybe\", \"\", \"yes\", want 3\nstdout: %q", got, result.Stdout)
	}
	if got := strings.Count(stdout, "differs between"); got != 1 {
		t.Errorf("backup printed the drift header %d times, want 1: a re-ask repeats the question, not the diff\nstdout: %q", got, result.Stdout)
	}
	// The third answer was taken, so the replace happened.
	expectContent(t, world.Mackup(".probrc"), "home\n")
}

func TestTheForceFlagsPreAnswerEveryPromptAndShowNone(t *testing.T) {
	// appspec/07: "--force / -f pre-answers every prompt with yes: no prompt
	// is shown, the guarded action proceeds"; "--force-no pre-answers every
	// prompt with no: no prompt is shown, the guarded action is skipped".
	//
	// Both runs have stdin at end-of-input, which is what makes "no prompt is
	// shown" structural rather than cosmetic: a program that printed the
	// question and then answered it itself would still pass a text assertion,
	// but one that actually READ stdin would fail here with the non-zero exit
	// appspec/07 gives end-of-input at a prompt. That property is the whole
	// reason --force is scriptable.
	for _, test := range []struct {
		flag string
		want string
	}{
		{"--force", "home\n"},
		{"--force-no", "storage\n"},
	} {
		world := newSyncWorld(t, ".probrc")
		world.WriteFile(".probrc", "home\n", 0o600)
		world.WriteMackup(".probrc", "storage\n", 0o600)

		result := world.Run(test.flag, "backup", probeKey).ExpectExit(0).ExpectSilentStderr()

		if strings.Contains(result.StdoutText(), answerHint) {
			t.Errorf("%s backup stdout = %q, want no prompt at all", test.flag, result.Stdout)
		}
		expectContent(t, world.Mackup(".probrc"), test.want)
	}
}

func TestEndOfInputAtAPromptEndsTheRunUnguarded(t *testing.T) {
	// appspec/07: "if a prompt is reached with no force flag and stdin reaches
	// end-of-input ... the program cannot obtain a valid answer and terminates
	// with a nonzero exit (an unhandled end-of-input condition -- the
	// unguarded regime)". Not an implicit no: a program answering for a user
	// who is not there is the same defect either way round, and the regime is
	// what says so -- appspec/07 gives guarded failures the "Error: " shape
	// and leaves the unguarded ones as the diagnostics of a run that fell
	// over.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", "home\n", 0o600)
	world.WriteMackup(".probrc", "storage\n", 0o600)

	before := world.Snapshot()
	result := world.Run("backup", probeKey).ExpectFailureExit()

	if strings.HasPrefix(result.StderrText(), "Error: ") || !strings.Contains(result.StderrText(), "mackup: ") {
		t.Errorf("backup stderr = %q, want the unguarded shape appspec/07 gives an unanswerable prompt, not a guarded \"Error: \" line", result.Stderr)
	}
	// Nothing was replaced: the prompt guards the mutation, so a run that
	// could not obtain an answer has not performed it.
	world.ExpectUnchanged(before)
}

// -- Dry run: appspec/01 section 3, one rule and its one exception -----------

func TestDryRunPrintsTheProgressLineAndMutatesNothingOnEitherPath(t *testing.T) {
	// appspec/01 section 3: dry-run "prints the progress line it would emit
	// for each acted-on file, then performs no copy, move, delete, or symlink
	// of any config file".
	//
	// Both of appspec/06's paths in one run, because they are different code:
	// a destination that does not exist (step 4, copy directly) and one that
	// exists and differs (step 3, compare then prompt). The second is the one
	// worth the fixture -- the prompt lives INSIDE the mutation it guards, so
	// a dry run must not ask either. Stdin is at end-of-input, so a program
	// that asked would fail here rather than being quietly answered.
	for _, test := range []struct {
		command string
		verb    string
		acted   string
		skipped string
	}{
		{"backup", backupVerb, ".fresh-home", ".fresh-storage"},
		{"restore", restoreVerb, ".fresh-storage", ".fresh-home"},
	} {
		world := newSyncWorld(t, ".conflict", ".fresh-home", ".fresh-storage")
		world.WriteFile(".conflict", "home side\n", 0o600)
		world.WriteMackup(".conflict", "storage side\n", 0o600)
		world.WriteFile(".fresh-home", "only in home\n", 0o600)
		world.WriteMackup(".fresh-storage", "only in storage\n", 0o600)

		before := world.Snapshot()
		result := world.Run("--dry-run", test.command, probeKey).ExpectExit(0).ExpectSilentStderr()

		stdout := result.StdoutText()
		for _, relative := range []string{".conflict", test.acted} {
			if want := test.verb + " " + relative + " ..."; !strings.Contains(stdout, want) {
				t.Errorf("%s --dry-run stdout = %q, want the progress line %q", test.command, result.Stdout, want)
			}
		}
		if strings.Contains(stdout, test.skipped) {
			t.Errorf("%s --dry-run stdout = %q, want no progress line for %s, whose source is not there",
				test.command, result.Stdout, test.skipped)
		}
		// The prompt and the diff above it are both inside "if not dry-run".
		for _, forbidden := range []string{answerHint, "differs between"} {
			if strings.Contains(stdout, forbidden) {
				t.Errorf("%s --dry-run stdout = %q, want no %q: dry-run stops at the progress line",
					test.command, result.Stdout, forbidden)
			}
		}
		world.ExpectUnchanged(before)
	}
}

func TestDryRunStillRunsTheFolderCreationGate(t *testing.T) {
	// The one exception appspec/01 section 3 carves out, in its own words:
	// "dry-run does not suppress the startup 'create the storage sub-folder'
	// decision for backup / link install -- that gate runs before the per-file
	// loop and, under a force flag, will still create the folder; absent a
	// force flag under dry-run it will still prompt."
	//
	// Both halves, because the rule is stated as a pair and a program keeping
	// only one of them is wrong in a way the other half cannot see. What the
	// exception does NOT extend to is the per-file loop, which is the second
	// assertion in the first half: the folder appears, and nothing lands in
	// it.
	forced, folder := newGateWorld(t, ".probrc")
	forced.WriteFile(".probrc", "home\n", 0o600)

	forced.Run("--dry-run", "--force", "backup", probeKey).ExpectExit(0).ExpectSilentStderr()

	if info, err := os.Stat(folder); err != nil || !info.IsDir() {
		t.Errorf("--dry-run --force backup left no Mackup folder at %s (%v): the environment gate is not a per-file mutation and runs regardless", folder, err)
	}
	expectAbsent(t, filepath.Join(folder, ".probrc"))

	asked, folder := newGateWorld(t, ".probrc")
	asked.WriteFile(".probrc", "home\n", 0o600)

	result := asked.RunWithInput("no\n", "--dry-run", "backup", probeKey).ExpectFailureExit()

	if !strings.Contains(result.StdoutText(), createFolderFirstLine) {
		t.Errorf("--dry-run backup stdout = %q, want the folder-creation prompt: dry-run does not suppress it", result.Stdout)
	}
	result.ExpectStderrLine(noHomeRefusal)
	expectAbsent(t, folder)
}

// -- The Mackup-folder gate: appspec/01 section 4 levels 2 and 3 -------------

func TestTheBackupGateCreatesTheMackupFolderOnYes(t *testing.T) {
	// Level 2, the ensure gate: "if absent, prompt 'Mackup needs a directory
	// to store your configuration files / Do you want to create it now?
	// <path>'; on yes, create it (recursively)".
	//
	// The path is part of the question. A prompt asking whether to create a
	// directory without saying which one is a prompt about the program's
	// configuration -- exactly the thing a user answering it is trying to
	// check.
	world, folder := newGateWorld(t, ".probrc")
	world.WriteFile(".probrc", "home\n", 0o600)

	result := world.RunWithInput("yes\n", "backup", probeKey).ExpectExit(0).ExpectSilentStderr()

	stdout := result.StdoutText()
	if !strings.Contains(stdout, createFolderFirstLine) {
		t.Fatalf("backup stdout = %q, want the first line of the folder prompt", result.Stdout)
	}
	if want := createFolderSecondLine + " " + folder + answerHint; !strings.Contains(stdout, want) {
		t.Errorf("backup stdout = %q, want the second line of the folder prompt to name the path: %q", result.Stdout, want)
	}
	// Created, and then used: the run carried on into the per-file loop
	// rather than stopping at the gate it had just satisfied.
	expectContent(t, filepath.Join(folder, ".probrc"), "home\n")
}

func TestDecliningTheFolderPromptIsTheNoHomeRefusal(t *testing.T) {
	// The other arm of level 2: "on no, fatal error 'Mackup can't do anything
	// without a home' and exit 1". appspec/07's error table gives the line
	// verbatim, including the =( at the end of it, and guarded means the
	// program says that and stops rather than falling over.
	//
	// Nothing is created and nothing is copied, which is the substance of the
	// refusal: a program that created the folder anyway and then declined to
	// use it would leave the user's storage changed by an answer that said no.
	world, folder := newGateWorld(t, ".probrc")
	world.WriteFile(".probrc", "home\n", 0o600)

	before := world.Snapshot()
	world.RunWithInput("no\n", "backup", probeKey).ExpectFailureExit().ExpectStderrLine(noHomeRefusal)

	expectAbsent(t, folder)
	world.ExpectUnchanged(before)
}

func TestRestoreRequiresTheMackupFolderAndCreatesNothing(t *testing.T) {
	// Level 3, the require gate: restore, link and link uninstall "require the
	// Mackup folder to already exist -- if absent, fatal error naming the
	// missing Mackup folder (with a hint to back up or sync first) and exit
	// 1".
	//
	// Creates nothing is the whole difference between the two levels, and it
	// is a contract rather than an omission: a fresh empty folder would make
	// an empty restore look like a successful one. Stdin is at end-of-input,
	// so a program that prompted here -- running level 2's gate for level 3's
	// command -- would fail for that reason too.
	world, folder := newGateWorld(t, ".probrc")

	before := world.Snapshot()
	result := world.Run("restore", probeKey).ExpectFailureExit().ExpectSilentStdout()

	result.ExpectStderr(missingFolderError + folder)
	expectAbsent(t, folder)
	world.ExpectUnchanged(before)
}

func TestAnUnknownApplicationIsRefusedBeforeAnyFolderIsCreatedOrPromptShown(t *testing.T) {
	// appspec/06 "Environment gate per command": "when an <application> is
	// named, its validity is checked BEFORE this gate, so an unknown app name
	// fails with 'Unsupported application: <name>' (exit 1) before any folder
	// is created or prompt shown."
	//
	// The world is one where the gate WOULD act: the Mackup folder is absent,
	// so a program that gated first would print the creation prompt and, with
	// stdin at end-of-input, fall over unguarded -- and a program that gated
	// first under --force would create a folder for a run that was never going
	// to do anything. The absent folder and the silent stdout are what pin the
	// order; the token alone would pass on either.
	world, folder := newGateWorld(t)

	before := world.Snapshot()
	world.Run("backup", "frobnicate").ExpectExit(1).
		ExpectStderrLine("Unsupported application: frobnicate").
		ExpectSilentStdout()

	expectAbsent(t, folder)
	world.ExpectUnchanged(before)
}

// -- Scope: appspec/01 section 3 and appspec/03's combined precedence --------

// storageSection is the [storage] section the harness's resolvable-storage
// world writes, spelled here so a case can rewrite the config with its own
// application lists without losing the engine that makes the world resolvable.
const storageSection = "[storage]\nengine = file_system\npath = storage\n"

func TestANamedApplicationOverridesBothConfiguredLists(t *testing.T) {
	// appspec/01 section 3: "a named app replaces the configured scope with
	// exactly that key and overrides both the allow and ignore lists (an
	// ignored app is still acted on when named)". This ticket's done-claim
	// puts it as "'backup vim' acts on vim while vim sits in
	// applications_to_ignore".
	//
	// The probe is kept out by BOTH lists at once -- absent from a non-empty
	// allowlist and present in the denylist -- so the single assertion below
	// fails on a program that honours either of them. A program that filtered
	// through the configured scope and then added the named key back would
	// pass this and fail nothing else, which is why scope.go does not filter
	// at all in this branch.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".mackup.cfg", storageSection+
		"[applications_to_sync]\nsomething-else\n"+
		"[applications_to_ignore]\n"+probeKey+"\n", 0o600)
	world.WriteFile(".probrc", "home\n", 0o600)

	world.Run("backup", probeKey).ExpectExit(0).ExpectSilentStderr()

	expectContent(t, world.Mackup(".probrc"), "home\n")
}

func TestTheDenylistWinsOverTheAllowlist(t *testing.T) {
	// appspec/03's combined precedence, which is the rule a reimplementation
	// is most likely to get backwards: "start with the allowlist if
	// [applications_to_sync] is present and non-empty ... remove every key in
	// [applications_to_ignore]". So an application in BOTH lists is ignored,
	// and the denylist is not a filter over the catalog but the last word.
	//
	// Three applications and three answers, because the two rules are only
	// separable together: `kept` is allowlisted and stays, `both` is in both
	// lists and goes, and `neither` is in no list and is out because a
	// non-empty allowlist is where the set STARTS. A program in which the
	// allowlist won would back up `both`; one that ignored the allowlist
	// entirely would back up `neither`.
	world := NewWorld(t)
	folder := world.UseMackupFolder()
	world.WriteFile(".mackup.cfg", storageSection+
		"[applications_to_sync]\nkept\nboth\n"+
		"[applications_to_ignore]\nboth\n", 0o600)
	for _, key := range []string{"kept", "both", "neither"} {
		world.WriteFile(".mackup/"+key+".cfg", probeDefinition("."+key+"-rc"), 0o600)
		world.WriteFile("."+key+"-rc", key+"\n", 0o600)
	}

	world.Run("backup").ExpectExit(0).ExpectSilentStderr()

	expectContent(t, filepath.Join(folder, ".kept-rc"), "kept\n")
	expectAbsent(t, filepath.Join(folder, ".both-rc"))
	expectAbsent(t, filepath.Join(folder, ".neither-rc"))
}

// -- The partial-failure contract: appspec/06 and appspec/01 section 5 -------

func TestAFileThatCannotBeCopiedIsReportedAndTheRunCarriesOnAndExitsOne(t *testing.T) {
	// appspec/06 "Partial-failure contract": a per-file copy failure "is not
	// fatal to the run: the program writes an error line to stderr ('Error:
	// Unable to copy <src> to <dst>: <reason>'), records the failed path as
	// data, and continues with the remaining files and applications", and at
	// the end writes "<Backup|Restore> incomplete: <N> file(s) could not be
	// copied:" followed by one indented line per failed path, and exits 1.
	//
	// Both directions, because the summary noun is a column of the direction
	// record. The failure is arranged by putting a REGULAR FILE where the
	// destination's parent directory belongs, which is portable and needs no
	// permission games: creating the parent fails, so the copy fails, and the
	// program is asked the question the contract is about -- does one bad file
	// end the run? The file after it in sorted order is the answer.
	for _, test := range []struct {
		command string
		summary string
	}{
		{"backup", backupIncomplete},
		{"restore", restoreIncomplete},
	} {
		world := newSyncWorld(t, ".probdir/inner", ".zprobrc")
		var source, destination string
		if test.command == "backup" {
			source = world.WriteFile(".probdir/inner", "home\n", 0o600)
			world.WriteFile(".zprobrc", "carried on\n", 0o600)
			world.WriteMackup(".probdir", "a file where the folder belongs\n", 0o600)
			destination = world.Mackup(".probdir", "inner")
		} else {
			source = world.WriteMackup(".probdir/inner", "storage\n", 0o600)
			world.WriteMackup(".zprobrc", "carried on\n", 0o600)
			world.WriteFile(".probdir", "a file where the folder belongs\n", 0o600)
			destination = world.Path(".probdir", "inner")
		}

		result := world.Run(test.command, probeKey).ExpectExit(1)

		stderr := result.StderrText()
		if want := copyFailurePrefix + source + " to " + destination + ": "; !strings.Contains(stderr, want) {
			t.Errorf("%s stderr = %q, want the per-file failure line %q", test.command, result.Stderr, want)
		}
		if want := test.summary + "1 file(s) could not be copied:"; !strings.Contains(stderr, want) {
			t.Errorf("%s stderr = %q, want the end-of-run summary %q", test.command, result.Stderr, want)
		}
		if want := "\n  " + source; !strings.Contains(stderr, want) {
			t.Errorf("%s stderr = %q, want the failed path listed under the summary as %q", test.command, result.Stderr, want)
		}
		// The run carried on: the file after the failure in sorted order was
		// copied, which is the whole of "failures flow up as data, not as
		// control flow".
		if test.command == "backup" {
			expectContent(t, world.Mackup(".zprobrc"), "carried on\n")
		} else {
			expectContent(t, world.Path(".zprobrc"), "carried on\n")
		}
	}
}

func TestTheIncompleteSummaryIsPrintedEvenWhenTheRunEndsAtAnUnanswerablePrompt(t *testing.T) {
	// A run can both fail a copy and then die at a prompt it cannot answer, and
	// the two contracts that cover those are separate: appspec/06 makes the
	// "<Backup|Restore> incomplete:" summary the end-of-run report of a run
	// that could not copy everything, and appspec/07 makes an unanswerable
	// prompt an unguarded termination. Ending the second way does not repeal
	// the first -- the summary is the only thing that names WHICH file needs
	// attention, and dropping it leaves the run's stderr saying less than the
	// failure it has already printed.
	//
	// Sorted order arranges it: ".probdir/inner" is copied (and fails, because
	// a regular file sits where its destination's parent belongs) before
	// ".zprobrc" is compared and prompted about. So a failure is recorded as
	// data at the moment the prompt cannot be answered.
	world := newSyncWorld(t, ".probdir/inner", ".zprobrc")
	source := world.WriteFile(".probdir/inner", "home\n", 0o600)
	world.WriteMackup(".probdir", "a file where the folder belongs\n", 0o600)
	world.WriteFile(".zprobrc", "home\n", 0o600)
	world.WriteMackup(".zprobrc", "storage\n", 0o600)

	result := world.Run("backup", probeKey).ExpectFailureExit()

	stderr := result.StderrText()
	if want := backupIncomplete + "1 file(s) could not be copied:"; !strings.Contains(stderr, want) {
		t.Errorf("backup stderr = %q, want the end-of-run summary %q even though the run ended at an unanswerable prompt", result.Stderr, want)
	}
	if want := "\n  " + source; !strings.Contains(stderr, want) {
		t.Errorf("backup stderr = %q, want the failed path listed under the summary as %q", result.Stderr, want)
	}
	// Still the unguarded termination, and still ahead of nothing: the summary
	// is the run's report and the diagnostic is its last word.
	if !strings.Contains(stderr, "mackup: ") {
		t.Errorf("backup stderr = %q, want the unguarded diagnostic appspec/07 gives an unanswerable prompt", result.Stderr)
	}
}

// -- Verbose: appspec/01 section 3 and appspec/07's header ------------------

func TestTheVerboseHeaderIsPrintedOnlyForAnApplicationThatPrintsSomething(t *testing.T) {
	// appspec/01 section 3 makes the per-app header one of the things verbose
	// ADDS, and appspec/06's step 1 skips a file whose source does not exist
	// SILENTLY. Together those decide this: the header belongs to the output it
	// heads, so an application that prints nothing is not announced.
	//
	// It matters at the scale the program actually runs at. Unscoped, the fan
	// out walks the whole shipped catalog, and on any real home nearly every
	// key has no file to copy -- so a header printed per key buries the few
	// lines the run is about. Measured before this was fixed: 623 stdout lines
	// for a home with one file in it, 614 of them headers.
	//
	// NO application is named, which is the only shape that can see it. Named,
	// there is exactly one application and it always has something to do, so an
	// eager header and a deferred one are the same bytes -- which is why the
	// two cases above this one cannot catch it and this one is not redundant
	// with them.
	world := NewWorld(t)
	world.UseMackupFolder()
	world.WriteFile(".mackup/alpha.cfg", "[application]\nname = Acting\n\n[configuration_files]\n.a-probrc\n", 0o600)
	world.WriteFile(".mackup/zulu.cfg", "[application]\nname = Quiet\n\n[configuration_files]\n.z-absent\n", 0o600)
	world.WriteFile(".a-probrc", "home\n", 0o600)

	result := world.Run("--verbose", "backup").ExpectExit(0).ExpectSilentStderr()

	stdout := result.StdoutText()
	if !strings.Contains(stdout, "--- Acting ---") {
		t.Errorf("--verbose backup stdout = %q, want the header for the application that copied a file", result.Stdout)
	}
	// Quiet's one file does not exist, so its per-file procedure skips
	// silently and it has nothing to head.
	if strings.Contains(stdout, "--- Quiet ---") {
		t.Errorf("--verbose backup stdout = %q, want NO header for an application whose files are all absent", result.Stdout)
	}

	// The claim generalised over the whole catalog, which is what makes this a
	// bound rather than two spot checks: every header is followed by the output
	// it heads. A header last in the run, or immediately followed by another
	// header, is a header for an application that said nothing.
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "--- ") {
			continue
		}
		if i+1 == len(lines) {
			t.Errorf("--verbose backup stdout = %q, want no trailing header: %q heads nothing", result.Stdout, line)
			continue
		}
		if strings.HasPrefix(lines[i+1], "--- ") {
			t.Errorf("--verbose backup stdout = %q, want no header immediately followed by another (%q then %q): the first heads nothing",
				result.Stdout, line, lines[i+1])
		}
	}
}

func TestVerbosePrintsTheLongProgressFormWithAbsolutePaths(t *testing.T) {
	// appspec/06's progress line in its second form: "<verb>\n  <src>\n
	// to\n  <dst> ...", which appspec/01 section 3 describes as swapping "short
	// progress lines for long ones (full absolute source/destination paths)".
	//
	// The short form must be GONE, not merely joined by the long one: the two
	// are one line in two spellings, and a program printing both would say
	// everything twice for every file in the run.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", "home\n", 0o600)

	result := world.Run("--verbose", "backup", probeKey).ExpectExit(0).ExpectSilentStderr()

	want := backupVerb + "\n  " + world.Path(".probrc") + "\n  to\n  " + world.Mackup(".probrc") + " ..."
	if !strings.Contains(result.StdoutText(), want) {
		t.Errorf("--verbose backup stdout = %q, want the long progress form %q", result.Stdout, want)
	}
	if strings.Contains(result.StdoutText(), backupVerb+" .probrc ...") {
		t.Errorf("--verbose backup stdout = %q, want the short progress line replaced by the long one, not joined by it", result.Stdout)
	}
}

func TestTheVerbosePerAppHeaderIsABoldNameBetweenBlueRules(t *testing.T) {
	// appspec/07: "per-app verbose header uses blue (34) rules around a bold
	// app name". Asserted against the raw bytes, because the claim IS the
	// escape sequences -- the stripped text is the same three words whatever
	// colour they were written in.
	//
	// The name is the definition's, not the key: the header is the label a
	// user reads to know whose files are scrolling past, and appspec/05 makes
	// the human name the thing a definition carries for that purpose.
	//
	// Verbose only. Without the flag the header is not printed at all, which
	// appspec/01 section 3 makes part of what verbose ADDS -- and which keeps
	// a bare backup from naming every application in the catalog whether it
	// had anything to do or not.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", "home\n", 0o600)

	result := world.Run("--verbose", "backup", probeKey).ExpectExit(0).ExpectSilentStderr()

	want := "\x1b[34m--- \x1b[1mProbe\x1b[0m\x1b[34m ---\x1b[0m"
	if !strings.Contains(result.Stdout, want) {
		t.Errorf("--verbose backup stdout = %q, want the header %q: blue rules around a bold name", result.Stdout, want)
	}

	plain := world.Run("backup", probeKey).ExpectExit(0)
	if strings.Contains(plain.StdoutText(), "--- Probe ---") {
		t.Errorf("backup stdout = %q, want no per-app header without --verbose", plain.Stdout)
	}
}

func TestVerboseSaysAFileIsAlreadyInSync(t *testing.T) {
	// The second of appspec/06's two verbose traces for this operation: "if
	// identical, skip (verbose prints '<f> already in sync, skipping')".
	//
	// Verbose is observationally pure (appspec/01 section 3: "it changes only
	// what is printed, never what is done or the exit code"), so the trace
	// arrives over a run that still does nothing at all -- which is what the
	// snapshot is for. A program that "checked" identity by copying and then
	// reporting it would satisfy the message and fail here.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", "same\n", 0o600)
	world.WriteMackup(".probrc", "same\n", 0o600)

	before := world.Snapshot()
	result := world.Run("--verbose", "backup", probeKey).ExpectExit(0).ExpectSilentStderr()

	if want := ".probrc already in sync, skipping"; !strings.Contains(result.StdoutText(), want) {
		t.Errorf("--verbose backup stdout = %q, want the trace %q", result.Stdout, want)
	}
	world.ExpectUnchanged(before)
}
