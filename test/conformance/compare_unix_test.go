//go:build conformance && unix

// The two comparison cases that need a path the process cannot read, which is
// a Unix permission question.
//
// Behind the `unix` build tag rather than guarded at runtime, for the reason
// harness_unix_test.go's FIFO is and internal/drift's own pair are: mode bits
// do not deny a read on every GOOS the toolchain compiles for, and a file that
// does not build says so more clearly than a case that silently passes.
// appspec/00 "Platform assumptions" targets Unix-like systems.
//
// Both make the MACKUP side unreadable rather than the home side. The world's
// snapshot walks the whole scratch root, so an unreadable directory under home
// would be a fixture that breaks the harness rather than one that tests the
// program; and the storage side is where an unreadable path actually turns up,
// on a Dropbox folder synced from another machine's account.

package conformance

import (
	"os"
	"strings"
	"testing"
)

// denyReads makes a path unreadable for the rest of the case, restoring it
// afterwards so the world's own cleanup can remove the tree, and skipping when
// the filesystem does not deny reads by mode at all.
//
// The superuser check is not ceremony: root reads a mode 0000 path, so the
// fixture would not be unreadable and the case would assert nothing.
// appspec/07's root guard means the program under test does not run this way,
// but `go test` can.
func denyReads(t *testing.T, path string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("the superuser reads a mode 0000 path, so this arrangement proves nothing")
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("making %s unreadable: %v", path, err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
	if _, err := os.Open(path); err == nil {
		t.Skip("this filesystem does not deny reads by mode, so the fixture is not unreadable")
	}
}

func TestAnUnreadableDestinationIsDifferingWithNoDiff(t *testing.T) {
	// appspec/06: "if either file is unreadable, treated as differing with no
	// detail (plain prompt)." Differing and not identical is the half that
	// matters -- a comparison that swallowed the read error and reported
	// agreement would skip the user's file and say nothing about why.
	//
	// So the assertion is the PROMPT, not a message: there is no message. A
	// program that reported agreement here prints nothing at all and exits 0,
	// which is indistinguishable from a correct run of an up-to-date file
	// unless the case knows a prompt was owed.
	world := newSyncWorld(t, ".probrc")
	world.WriteFile(".probrc", "home content\n", 0o600)
	unreadable := world.WriteMackup(".probrc", "storage content\n", 0o600)
	denyReads(t, unreadable)

	stdout := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0).StdoutText()

	if want := "Are you sure that you want to replace it?"; !strings.Contains(stdout, want) {
		t.Errorf("backup stdout = %q, want the replace prompt: an unreadable file is differing, not identical", stdout)
	}
	// The header is printed only when there is detail, so its absence is how
	// "no diff detail" is observable from out here.
	if strings.Contains(stdout, "differs between") {
		t.Errorf("backup stdout = %q, want the plain prompt with no drift header: this pair has no detail", stdout)
	}
}

func TestAnUnreadableDirectoryInsideATreeIsAChangedEntryAndNotTheEndOfTheWalk(t *testing.T) {
	// A subdirectory that cannot be read is a real difference the user should
	// be told about, and it is not a reason to abandon everything else the
	// comparison found.
	//
	// The second half is what the fixture is built for. There is a changed
	// file BESIDE the unreadable directory, so a walk that gave up at the
	// unreadable one would report a shorter list -- and a case with only the
	// unreadable directory in it could not tell "recorded and carried on"
	// apart from "recorded and stopped", because there would be nothing left
	// to stop before.
	world := newSyncWorld(t, ".probdir")
	world.WriteFile(".probdir/closed/inside", "same\n", 0o600)
	world.WriteFile(".probdir/open", "home version\n", 0o600)
	world.WriteMackup(".probdir/closed/inside", "same\n", 0o600)
	world.WriteMackup(".probdir/open", "storage version\n", 0o600)
	denyReads(t, world.Mackup(".probdir", "closed"))

	stdout := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0).StdoutText()

	for _, want := range []string{"changed: closed", "changed: open"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("backup stdout = %q, want the detail line %q", stdout, want)
		}
	}
}
