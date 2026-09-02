//go:build unix

package syncfs

import (
	"path/filepath"
	"syscall"
	"testing"
)

// The cases that need the process umask fixed, which is a Unix concept and a
// process-wide one.
//
// Behind the `unix` build tag rather than guarded at runtime, the way
// test/conformance/harness_unix_test.go is for its FIFO: syscall.Umask does not
// exist on every GOOS the toolchain can compile for, and a file that does not
// build is a clearer statement than a case that silently skips. appspec/00
// "Platform assumptions" targets Unix-like systems, so nothing is lost.

// withUmask fixes the process umask for one case and restores it afterwards.
//
// The umask is process-wide, so a case using this must not run in parallel with
// any case that creates a file. None in this package call t.Parallel, and this
// comment is the reason to keep it that way.
func withUmask(t *testing.T, mask int) {
	t.Helper()
	previous := syscall.Umask(mask)
	t.Cleanup(func() { syscall.Umask(previous) })
}

// TestCopyCreatesMissingParentsWithoutClampingThem pins parentMode: the
// ancestors a copy creates to reach its destination get the process umask
// applied to 0777, not the 0700 the clamp gives a directory that was copied.
//
// The umask is fixed at 022 so that 0755 is an assertion rather than an
// observation of whatever the machine happened to be set to. Without that this
// case cannot exist at all: under a umask of 077 a created parent is 0700
// either way, and the mutation that clamps the parents is invisible.
//
// Why the distinction is worth a case rather than a comment: appspec/06 clamps
// what is copied, and the Mackup folder's own children are not copied -- they
// are made on the way to a file. Narrowing them would change the permissions of
// the shared storage tree on the first backup, silently, on a folder the user
// may have deliberately made group-readable to sync it.
func TestCopyCreatesMissingParentsWithoutClampingThem(t *testing.T) {
	withUmask(t, 0o022)

	root := t.TempDir()
	src := filepath.Join(root, "src", ".config-file")
	dst := filepath.Join(root, "dst", "app", "deeper", ".config-file")
	writeFile(t, src, 0o600, "content")

	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	expectPerm(t, filepath.Join(root, "dst", "app"), 0o755)
	expectPerm(t, filepath.Join(root, "dst", "app", "deeper"), 0o755)
	expectPerm(t, dst, 0o600)
}

// TestLinkCreatesMissingParentsWithoutClampingThem is the same claim for the
// other primitive that creates a destination's ancestors.
//
// Both are asserted rather than one, because the two primitives call
// os.MkdirAll separately: appspec/06 states the parent-creation clause once for
// copy and once for link, and a change to parentMode that reached only one of
// them would leave the program creating the same directory two different ways
// depending on which command got there first.
func TestLinkCreatesMissingParentsWithoutClampingThem(t *testing.T) {
	withUmask(t, 0o022)

	root := t.TempDir()
	target := filepath.Join(root, "storage", ".config-file")
	linkPath := filepath.Join(root, "home", "app", "deeper", ".config-file")
	writeFile(t, target, 0o600, "content")

	if err := Link(target, linkPath); err != nil {
		t.Fatalf("Link: %v", err)
	}
	expectPerm(t, filepath.Join(root, "home", "app"), 0o755)
	expectPerm(t, filepath.Join(root, "home", "app", "deeper"), 0o755)
	expectPerm(t, target, 0o600)
}
