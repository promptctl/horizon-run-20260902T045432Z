//go:build conformance && unix

// The folder-gate cases that need a directory the process cannot search, which
// is a Unix permission question.
//
// Behind the `unix` build tag rather than guarded at runtime, for the reason
// compare_unix_test.go and harness_unix_test.go give: mode bits do not deny a
// stat on every GOOS the toolchain compiles for, and a file that does not build
// says so more clearly than a case that silently passes. appspec/00 "Platform
// assumptions" targets Unix-like systems.

package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// denySearch removes a directory's search bit for the rest of the case,
// restoring it afterwards so the world's own cleanup can remove the tree.
//
// The superuser check is not ceremony, and it is the same one denyReads makes:
// root searches a directory with no execute bit, so the fixture would deny
// nothing and the case would assert nothing.
//
// child is the name of something that EXISTS inside dir, and the last check
// stats it to confirm the mode actually denied the search rather than assuming
// the filesystem enforces one. It is a parameter because the two callers pass
// different trees: a probe path hardcoded for one of them stats a path that is
// simply absent for the other, which returns ENOENT forever and makes a guard
// that can never fire look like one that never needed to.
func denySearch(t *testing.T, dir, child string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("the superuser searches a directory with no execute bit, so this arrangement proves nothing")
	}
	if _, err := os.Stat(filepath.Join(dir, child)); err != nil {
		t.Fatalf("the fixture is wrong, not the filesystem: %s is not there to be denied: %v",
			filepath.Join(dir, child), err)
	}
	if err := os.Chmod(dir, 0o600); err != nil {
		t.Fatalf("removing the search bit from %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := os.Stat(filepath.Join(dir, child)); err == nil {
		t.Skip("this filesystem does not deny a search by mode, so the fixture denies nothing")
	}
}

func TestAMackupFolderThatCannotBeInspectedIsNotReportedAsMissing(t *testing.T) {
	// appspec/01 section 4 levels 2 and 3 both ask whether the Mackup folder is
	// there. A stat that fails for any reason other than ENOENT has not
	// answered, and answering anyway is how each gate came to assert something
	// the program never established.
	//
	// The arrangement is the storage ROOT without its search bit, with the
	// folder sitting inside it. That is what makes this a gate case and not a
	// config one: level 1 stats the root itself, which needs search on the
	// root's PARENT and so still passes, and only the stat one level deeper
	// fails. A user reaches it through a Dropbox tree synced with modes from
	// another machine's account.
	//
	// Both directions, because the two gates read the same predicate and had
	// the same bug in different clothes. Restore said "Unable to find the
	// Mackup folder" -- with the hint that sends the user to re-run backup on
	// another machine, advice for a folder that is sitting right there. Backup
	// offered to CREATE the folder that already exists, which is why its stdout
	// is asserted silent: the prompt is the whole defect on that side, and an
	// exit code alone cannot see it, since a declined or unanswerable prompt
	// exits non-zero too.
	for _, command := range []string{"backup", "restore"} {
		world := newSyncWorld(t, ".probrc")
		world.WriteMackup(".probrc", "storage\n", 0o600)
		denySearch(t, world.Path("storage"), "Mackup")

		result := world.Run(command, probeKey).ExpectExit(1).ExpectSilentStdout()

		stderr := result.StderrText()
		if !strings.Contains(stderr, uninspectableFolderError) {
			t.Errorf("%s stderr = %q, want %q naming the stat that failed", command, result.Stderr, uninspectableFolderError)
		}
		if strings.Contains(stderr, missingFolderError) {
			t.Errorf("%s stderr = %q, want no %q: the folder is there and the program did not establish otherwise",
				command, result.Stderr, missingFolderError)
		}
		if strings.Contains(result.StdoutText(), createFolderSecondLine) {
			t.Errorf("%s stdout = %q, want no offer to create a folder that already exists", command, result.Stdout)
		}
	}
}

func TestAStorageRootThatCannotBeInspectedIsNotReportedAsMissing(t *testing.T) {
	// Level 1 of the same lattice, and the same conflation: appspec/01 section
	// 4's storage-root check answered "Unable to find the storage folder" for
	// any stat error at all, so a root the program could not look at was
	// reported as one that is not there.
	//
	// The root is put one directory DOWN and that directory loses its search
	// bit, which is what separates this from the gate case above: there the
	// root itself was searchable and only the folder inside it was not, so
	// level 1 passed and level 2/3 failed. Here level 1 is the one that cannot
	// stat, and it must say so rather than send the user looking for a
	// directory that exists.
	//
	// The message is the whole assertion, because both spellings exit 1 -- a
	// case that watched only the exit code would agree with the defect.
	world := NewWorld(t)
	world.WriteFile(".mackup.cfg", "[storage]\nengine = file_system\npath = outer/storage\n", 0o600)
	if err := os.MkdirAll(world.Path("outer", "storage", "Mackup"), 0o700); err != nil {
		t.Fatalf("creating the storage root: %v", err)
	}
	world.WriteFile(".mackup/"+probeKey+".cfg", probeDefinition(".probrc"), 0o600)
	denySearch(t, world.Path("outer"), "storage")

	result := world.Run("restore", probeKey).ExpectExit(1).ExpectSilentStdout()

	stderr := result.StderrText()
	if !strings.Contains(stderr, uninspectableRootError) {
		t.Errorf("restore stderr = %q, want %q naming the stat that failed", result.Stderr, uninspectableRootError)
	}
	if strings.Contains(stderr, missingRootError) {
		t.Errorf("restore stderr = %q, want no %q: the root is there and the program did not establish otherwise",
			result.Stderr, missingRootError)
	}
}
