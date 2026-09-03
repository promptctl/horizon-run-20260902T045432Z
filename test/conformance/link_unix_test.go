//go:build conformance && unix

// The link cases that need a directory the process cannot search or cannot
// write to, which are Unix permission questions.
//
// Behind the `unix` build tag rather than guarded at runtime, for the reason
// sync_unix_test.go and compare_unix_test.go give: mode bits do not deny a stat
// on every GOOS the toolchain compiles for, and a file that does not build says
// so more clearly than a case that silently passes. appspec/00 "Platform
// assumptions" targets Unix-like systems.

package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAHomePathThatCannotBeInspectedIsSkippedRatherThanFailingTheRun(t *testing.T) {
	// This is the one place in the program where a stat error is deliberately
	// read as "not there", and it is the opposite of what backup does with the
	// same condition one file over (TestASourceThatCannotBeInspectedIsAFailure-
	// AndNotAnAbsence). The case exists so that the difference is a decision
	// with a test behind it rather than an inconsistency waiting to be tidied
	// away in either direction.
	//
	// Why the copy-side answer does not transfer, in the specification's own
	// terms. Backup's fix rests on appspec/01 section 5's unconditional "A
	// partial backup/restore can never exit 0", so a copy run that silently
	// skipped an unreadable source and exited 0 contradicted the specification
	// itself. appspec/00 promise 9 then withholds exactly that guarantee from
	// this strategy -- it is titled "(copy mode)" and says "this honesty is
	// asymmetric -- the link strategy does not uphold it as cleanly" -- and
	// appspec/07's error table has no row for an uninspectable home path under
	// any link command. Section 5's permission to improve link failures is
	// bounded by "without changing any successful-run behavior", and the
	// reference completes this run and exits 0.
	//
	// So: exit 0, no diagnostic, and the run carries on to the next file in
	// sorted order. The verbose trace is what keeps the skip observable at all,
	// and it is asserted here because silence plus exit 0 with nothing said is
	// the outcome that would make this decision indefensible rather than merely
	// conservative.
	world := newSyncWorld(t, ".probdir/inner", ".zprobrc")
	world.WriteFile(".probdir/inner", "cannot be read\n", 0o600)
	world.WriteFile(".zprobrc", "carried on\n", 0o600)
	denySearch(t, world.Path(".probdir"), "inner")

	result := world.Run("--verbose", "link", "install", probeKey).
		ExpectExit(0).ExpectSilentStderr()

	if want := doingNothing + "\n  " + world.Path(".probdir", "inner") + "\n"; !strings.Contains(result.StdoutText(), want) {
		t.Errorf("link install stdout = %q, want the skip trace %q for a home path it could not stat", result.Stdout, want)
	}
	if strings.Contains(result.StdoutText(), linkVerb+" .probdir/inner") {
		t.Errorf("link install stdout = %q, want no progress line for a file it did not act on", result.Stdout)
	}
	// The run carried on rather than stopping at the file it could not read,
	// which is the half that distinguishes a skip from the fail-hard regime of
	// appspec/01 section 5.
	expectLinkedInto(t, world.Path(".zprobrc"), world.Mackup(".zprobrc"), "carried on\n")
}

func TestAMackupPathThatCannotBeInspectedIsSkippedByLink(t *testing.T) {
	// The other end of the decision recorded at linkableSource, under the other
	// door. `link` reads FROM storage, so appspec/06 step 1's "act only if ...
	// the mackup path exists as a regular file or directory" is asked of the
	// mackup path here -- and a stat that fails for a reason other than absence
	// is read as an absence, which is the same two-valued answer link install
	// gives the home path and the opposite of what backup gives its source.
	//
	// The argument that licenses it is linkableSource's and is not repeated
	// here, but this side has a second one that link install's does not: the
	// skip touches nothing at all. Nothing is created, nothing is deleted, and
	// the home file the user already has stays exactly as it was. Proceeding
	// instead -- on a storage path the program could not inspect -- would
	// delete a real home file and point the home path at something unknown,
	// which is the one outcome appspec/00 promise 10 rules out.
	//
	// So: exit 0, no diagnostic, and the run carries on to the next file in
	// sorted order. The verbose trace is what keeps the skip observable, and it
	// names the STORAGE path, because storage is the side that failed the
	// condition.
	world := newSyncWorld(t, ".probdir/inner", ".zprobrc")
	world.WriteMackup(".probdir/inner", "unreadable\n", 0o600)
	world.WriteMackup(".zprobrc", "carried on\n", 0o600)
	world.WriteFile(".probdir/inner", "the user's own\n", 0o600)
	denySearch(t, world.Mackup(".probdir"), "inner")

	result := world.Run("--verbose", "link", probeKey).
		ExpectExit(0).ExpectSilentStderr()

	if want := doingNothing + "\n  " + world.Mackup(".probdir", "inner") + "\n"; !strings.Contains(result.StdoutText(), want) {
		t.Errorf("link stdout = %q, want the skip trace %q for a storage path it could not stat", result.Stdout, want)
	}
	if strings.Contains(result.StdoutText(), joinVerb+" .probdir/inner") {
		t.Errorf("link stdout = %q, want no progress line for a file it did not act on", result.Stdout)
	}
	// The home file the skip protected is untouched, and the run carried on
	// rather than stopping at the file it could not read -- the half that
	// distinguishes a skip from the fail-hard regime of appspec/01 section 5.
	expectRealFile(t, world.Path(".probdir", "inner"))
	expectContent(t, world.Path(".probdir", "inner"), "the user's own\n")
	expectLinkedIntoUnchangedStorage(t, world.Path(".zprobrc"), world.Mackup(".zprobrc"), "carried on\n")
}

// denyWrites removes a directory's write bit for the rest of the case,
// restoring it afterwards so the world's own cleanup can remove the tree.
//
// denySearch's sibling, and it makes the same two checks for the same reasons:
// the superuser writes into a directory with no write bit, so the fixture would
// deny nothing; and the denial is confirmed against the filesystem rather than
// assumed, since not every filesystem this suite might run on enforces mode
// bits. It is confirmed with the operation the case is about -- creating a
// symlink -- rather than with a regular file, so that what is verified is what
// is relied on.
func denyWrites(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("the superuser writes into a directory with no write bit, so this arrangement proves nothing")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("removing the write bit from %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	probe := filepath.Join(dir, "probe-write")
	if err := os.Symlink("/nowhere", probe); err == nil {
		_ = os.Remove(probe)
		t.Skip("this filesystem does not deny a write by mode, so the fixture denies nothing")
	}
}

func TestLinkClampsItsStorageTargetBeforeTryingToCreateTheLink(t *testing.T) {
	// appspec/06 "link(target, link_path)": "BEFORE LINKING, target's
	// permissions are set recursively." The word is "before", and this is the
	// only case in the suite that can tell before from after.
	//
	// Everywhere else the order is unobservable, and the reason is worth
	// stating because it is what made this case necessary rather than
	// gold-plating. A successful run ends with the target clamped either way,
	// so the two orders agree on every outcome the other cases look at. Under
	// backup, restore and link install they agree even harder: the target is a
	// file syncfs.Copy wrote moments earlier, and Copy clamps what it writes,
	// so the clamp inside Link has nothing left to do at all.
	//
	// `link` is where that stops being true -- the target is a file the run did
	// not create -- and a link that FAILS is where the order becomes visible.
	// Clamp-then-symlink leaves the storage file at 0600 with the run reporting
	// failure; symlink-then-clamp leaves it at the 0644 it arrived with. The
	// mode after a failed run is the whole assertion.
	//
	// The arrangement is a home directory the process may search but not write
	// to, which is what a config directory owned by another account looks like.
	// The stat of the home path inside it succeeds and reports absence -- so
	// the procedure takes step 4 and reaches syncfs.Link -- and the symlink
	// then fails. Nothing about the storage side is denied, so the clamp is
	// free to run if the program asks for it in the right order.
	world := newSyncWorld(t, ".probdir/inner")
	world.WriteMackup(".probdir/inner", "shared\n", 0o644)
	if err := os.MkdirAll(world.Path(".probdir"), 0o700); err != nil {
		t.Fatalf("creating the home directory the link would land in: %v", err)
	}
	denyWrites(t, world.Path(".probdir"))

	result := world.Run("link", probeKey).ExpectFailureExit()

	if stderr := result.StderrText(); !strings.Contains(stderr, world.Path(".probdir", "inner")) {
		t.Errorf("link stderr = %q, want a diagnostic naming the link %s it could not create", result.Stderr, world.Path(".probdir", "inner"))
	}
	expectAbsent(t, world.Path(".probdir", "inner"))
	expectMode(t, world.Mackup(".probdir", "inner"), 0o600)
	expectContent(t, world.Mackup(".probdir", "inner"), "shared\n")
}
