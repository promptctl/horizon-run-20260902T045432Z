//go:build unix

package drift

import (
	"os"
	"path/filepath"
	"testing"
)

// The one case that needs a file the process cannot read, which is a Unix
// permission question.
//
// Behind the `unix` build tag rather than guarded at runtime, the way
// internal/syncfs's umask cases and test/conformance/harness_unix_test.go's
// FIFO are: mode bits do not deny a read on every GOOS the toolchain compiles
// for, and a file that does not build says that more clearly than a case that
// silently passes. appspec/00 "Platform assumptions" targets Unix-like systems.

func TestAnUnreadableFileIsDifferingWithNoDetail(t *testing.T) {
	// appspec/06: "If either file is unreadable, treated as differing with no
	// detail (plain prompt)." Differing and not identical is the half that
	// matters -- a comparison that swallowed the read error and reported
	// agreement would skip the user's file without telling them why.
	if os.Geteuid() == 0 {
		// The superuser reads a 0000 file, so the fixture would not be
		// unreadable and the case would assert nothing. appspec/07's root
		// guard means the program does not run this way, but `go test` can.
		t.Skip("the superuser can read a mode 0000 file, so this arrangement proves nothing")
	}

	dir := t.TempDir()
	source := writeFile(t, filepath.Join(dir, "source"), "alpha\n")
	target := writeFile(t, filepath.Join(dir, "target"), "bravo\n")
	if err := os.Chmod(target, 0o000); err != nil {
		t.Fatalf("making %s unreadable: %v", target, err)
	}
	// The fixture is checked rather than assumed: a chmod that did not take --
	// on a filesystem mounted without permissions, say -- would leave a case
	// that reports a pass over an ordinary readable pair.
	if _, err := os.ReadFile(target); err == nil {
		t.Skip("this filesystem does not deny reads by mode, so the fixture is not unreadable")
	}

	for _, pair := range []struct {
		why            string
		source, target string
	}{
		{"an unreadable destination", source, target},
		{"an unreadable source", target, source},
	} {
		got := Compare(pair.source, pair.target)
		expectDiffers(t, got, pair.why)
		if len(got.Detail) != 0 {
			t.Errorf("%s printed detail, and appspec/06 gives this case the plain prompt: %v", pair.why, details(got))
		}
	}
}

func TestAnUnreadableDirectoryInsideATreeIsAChangedEntryRatherThanTheEndOfTheWalk(t *testing.T) {
	// A subdirectory that cannot be read is a real difference the user should
	// be told about, and it is not a reason to abandon what the rest of the
	// comparison found. The second half is what the case is for: the changed
	// name beside it must still be reported.
	if os.Geteuid() == 0 {
		t.Skip("the superuser can read a mode 0000 directory, so this arrangement proves nothing")
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "source", "closed", "inside"), "1\n")
	writeFile(t, filepath.Join(dir, "target", "closed", "inside"), "1\n")
	writeFile(t, filepath.Join(dir, "source", "open"), "one\n")
	writeFile(t, filepath.Join(dir, "target", "open"), "two\n")

	closed := filepath.Join(dir, "target", "closed")
	if err := os.Chmod(closed, 0o000); err != nil {
		t.Fatalf("making %s unreadable: %v", closed, err)
	}
	// Restored so that t.TempDir's own cleanup can remove the tree.
	t.Cleanup(func() { _ = os.Chmod(closed, 0o700) })
	if _, err := os.ReadDir(closed); err == nil {
		t.Skip("this filesystem does not deny reads by mode, so the fixture is not unreadable")
	}

	got := Compare(filepath.Join(dir, "source"), filepath.Join(dir, "target"))
	expectDiffers(t, got, "a tree holding a directory that cannot be read")
	if want := []string{"changed: closed", "changed: open"}; !equalLines(details(got), want) {
		t.Errorf("detail = %v, want %v", details(got), want)
	}
}

func TestARootThatCannotBeReadIsDifferingWithNoDetail(t *testing.T) {
	// The directory arm's own version of the unreadable-file rule. There is
	// nothing true to say about which files differ, so this is the plain
	// prompt rather than a list.
	if os.Geteuid() == 0 {
		t.Skip("the superuser can read a mode 0000 directory, so this arrangement proves nothing")
	}

	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	writeFile(t, filepath.Join(source, "f"), "1\n")
	writeFile(t, filepath.Join(target, "f"), "1\n")
	if err := os.Chmod(target, 0o000); err != nil {
		t.Fatalf("making %s unreadable: %v", target, err)
	}
	t.Cleanup(func() { _ = os.Chmod(target, 0o700) })
	if _, err := os.ReadDir(target); err == nil {
		t.Skip("this filesystem does not deny reads by mode, so the fixture is not unreadable")
	}

	got := Compare(source, target)
	expectDiffers(t, got, "a destination directory that cannot be read")
	if len(got.Detail) != 0 {
		t.Errorf("detail printed for a tree that could not be walked: %v", details(got))
	}
}
