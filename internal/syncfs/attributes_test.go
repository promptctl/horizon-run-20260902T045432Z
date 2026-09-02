package syncfs

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// A cleanupCall is one invocation of a cleanup command as a case observed it,
// together with what the filesystem looked like AT THE MOMENT of the call.
//
// The observation is what makes appspec/06's ordering claim testable. "Before
// deleting or chmod-ing, the program strips filesystem attributes" is a
// statement about order, and order leaves no trace in the end state: a cleanup
// run after the delete removes the same attributes from nothing and the run
// finishes looking identical. Recording whether the path was still there, and
// what mode it carried, is how the two are told apart.
type cleanupCall struct {
	Argv    []string
	Existed bool
	Mode    fs.FileMode
}

// recordCleanups replaces the cleanup runner for the duration of one case and
// returns the calls it collects, so that nothing is spawned and the commands
// asked for are observable.
//
// It is the seam runCleanup exists for. The alternative -- letting the real
// commands run -- observes nothing: they are best-effort, their output is
// discarded, and on a machine without setfacl installed they do not run at all,
// so a case built on them would pass over a program that had stopped asking.
func recordCleanups(t *testing.T) *[]cleanupCall {
	t.Helper()
	var calls []cleanupCall
	previous := runCleanup
	runCleanup = func(argv []string) {
		call := cleanupCall{Argv: append([]string(nil), argv...)}
		// Lstat, not Stat: the path being cleaned may be a symlink about to be
		// deleted as a link, and following it would report the target's
		// existence instead.
		if info, err := os.Lstat(argv[len(argv)-1]); err == nil {
			call.Existed, call.Mode = true, info.Mode().Perm()
		}
		calls = append(calls, call)
	}
	t.Cleanup(func() { runCleanup = previous })
	return &calls
}

// TestTheCleanupCommandsAreTheOnesAppspec06NamesForEachPlatform pins the table
// of appspec/06 "Attribute cleanup" literally, per operating system.
//
// Literal argv rather than a check that "some command was chosen", because the
// flags are the whole content: `chmod -N` and `setfacl -b` are two vendors'
// spellings of the same operation and neither works on the other's platform,
// and `chattr -f` is what makes that one quiet about a filesystem that does not
// support the attribute. A wrong flag here fails silently forever, since these
// commands' failures are discarded by design.
func TestTheCleanupCommandsAreTheOnesAppspec06NamesForEachPlatform(t *testing.T) {
	const path = "/some/config/path"
	for _, tc := range []struct {
		goos string
		want [][]string
	}{
		{
			goos: "darwin",
			want: [][]string{
				{"/bin/chmod", "-R", "-N", path},
				{"/usr/bin/chflags", "-R", "nouchg", path},
			},
		},
		{
			goos: "linux",
			want: [][]string{
				{"/bin/setfacl", "-R", "-b", path},
				{"/usr/bin/chattr", "-R", "-f", "-i", path},
			},
		},
		{
			// appspec/00 "Platform assumptions" names macOS and Linux and no
			// third platform, and neither specification gives a command for
			// one. Nothing is spawned rather than a guess being made.
			goos: "freebsd",
			want: nil,
		},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			if got := attributeCleanups(tc.goos, path); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("attributeCleanups(%q) = %v, want %v", tc.goos, got, tc.want)
			}
		})
	}
}

// TestACleanupCommandRunsWhenItsBinaryExistsAndIsSilentWhenItDoesNot exercises
// the real runner -- a real subprocess, no seam -- because "the cleanup step is
// skipped" is a claim about spawning, and a case that replaced the spawn could
// not make it. The marker file is the evidence in both directions: present
// after the run that should have happened, absent after the one that should
// not. Asserting only that the absent-binary call returned quietly would pass
// over a runner that spawned nothing at all in either case.
//
// What this case does NOT observe, stated because the name once claimed it did:
// it cannot see runCleanupCommand's os.Stat guard. An absent binary produces no
// marker whether the guard is there or not, since exec's own ENOENT is
// discarded to the same effect -- confirmed by injection, which left this case
// passing over a runner with the guard removed. The post-condition appspec/06
// states ("if the binary is absent, that cleanup step is skipped": nothing
// happens, and nothing fails) is what is asserted here, and it is all that is
// observable. runCleanupCommand's own comment records why the guard is kept
// anyway.
func TestACleanupCommandRunsWhenItsBinaryExistsAndIsSilentWhenItDoesNot(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "marker")
	script := filepath.Join(root, "cleanup.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch \"$1\"\n"), 0o700); err != nil {
		t.Fatalf("writing the stand-in binary: %v", err)
	}

	runCleanupCommand([]string{script, marker})
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the cleanup command did not run: %v", err)
	}

	if err := os.Remove(marker); err != nil {
		t.Fatalf("clearing the marker: %v", err)
	}
	runCleanupCommand([]string{script + ".absent", marker})
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a cleanup command whose binary is absent ran anyway: %v", err)
	}
}

// TestACleanupCommandThatFailsIsNotReported pins the other half of best-effort:
// a binary that exists and exits non-zero is not a failure of the sync.
//
// runCleanupCommand returns nothing, so the assertion is that the call
// completes -- a version that panicked, or that grew an error return the
// callers would have to decide about, fails to build or fails here. appspec/06
// leaves nothing for a caller to do with such an error: the chmod or delete
// that follows reports its own failure through the partial-failure contract.
func TestACleanupCommandThatFailsIsNotReported(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "failing.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 7\n"), 0o700); err != nil {
		t.Fatalf("writing the stand-in binary: %v", err)
	}
	runCleanupCommand([]string{script, filepath.Join(root, "path")})
}

// requireCleanupPlatform skips a case that needs at least one cleanup command
// to exist for the host.
//
// appspec/06 gives commands for macOS and Linux only, so on any other GOOS the
// ordering cases below have nothing to observe and would pass vacuously. A skip
// says that out loud rather than reporting coverage the run does not have.
func requireCleanupPlatform(t *testing.T) {
	t.Helper()
	if attributeCleanups(runtime.GOOS, "/path") == nil {
		t.Skipf("appspec/06 names no attribute-cleanup commands for %s", runtime.GOOS)
	}
}

// TestDeleteStripsAttributesWhileThePathIsStillThere pins the first clause of
// appspec/06 "delete(path)" as an ORDER, not merely as a pair of things that
// both happen.
//
// The point of stripping first is that an immutable flag or a restrictive ACL
// is exactly what would make the removal fail. A cleanup issued after the
// removal is issued against a path that is gone, which is why the recorded
// observation is "the path still existed when the command was asked for".
func TestDeleteStripsAttributesWhileThePathIsStillThere(t *testing.T) {
	requireCleanupPlatform(t)
	calls := recordCleanups(t)

	root := t.TempDir()
	path := filepath.Join(root, ".config-file")
	writeFile(t, path, 0o600, "content")

	if err := Delete(path); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	want := attributeCleanups(runtime.GOOS, path)
	if len(*calls) != len(want) {
		t.Fatalf("Delete asked for %d cleanup commands, want %d", len(*calls), len(want))
	}
	for i, call := range *calls {
		if !reflect.DeepEqual(call.Argv, want[i]) {
			t.Errorf("cleanup %d was %v, want %v", i, call.Argv, want[i])
		}
		if !call.Existed {
			t.Errorf("cleanup %d ran after the path had already been removed", i)
		}
	}
}

// TestClampStripsAttributesBeforeItChangesTheMode pins the same ordering claim
// for the other primitive appspec/06 names -- "before deleting or CHMOD-ing" --
// and observes it the way the delete case does, through the state the
// filesystem was in when the command was asked for.
//
// The fixture starts at 0644 so that the recorded mode distinguishes the two
// orders: 0644 means the cleanup came first, 0600 means the chmod did.
func TestClampStripsAttributesBeforeItChangesTheMode(t *testing.T) {
	requireCleanupPlatform(t)
	calls := recordCleanups(t)

	root := t.TempDir()
	path := filepath.Join(root, ".config-file")
	writeFile(t, path, 0o644, "content")

	if err := Clamp(path); err != nil {
		t.Fatalf("Clamp: %v", err)
	}
	if len(*calls) == 0 {
		t.Fatalf("Clamp asked for no cleanup commands")
	}
	for i, call := range *calls {
		if call.Mode != 0o644 {
			t.Errorf("cleanup %d saw mode %04o, want 0644 -- the chmod ran first", i, call.Mode)
		}
	}
	expectPerm(t, path, 0o600)
}

// TestCopyAndLinkInheritTheAttributeStripThroughTheClamp pins the reach of
// appspec/06's "This precondition is shared by the delete and chmod primitives,
// so it holds across all operations that remove or overwrite".
//
// Copy and Link do not call the cleanup themselves; they get it because the
// clamp is a post-condition of both. That is a structural claim, and the case
// exists so that a later refactor which inlines a chmod into either of them --
// dropping the cleanup on the way -- is not silent.
func TestCopyAndLinkInheritTheAttributeStripThroughTheClamp(t *testing.T) {
	requireCleanupPlatform(t)

	t.Run("copy strips the destination", func(t *testing.T) {
		calls := recordCleanups(t)
		root := t.TempDir()
		src := filepath.Join(root, "src", ".config-file")
		dst := filepath.Join(root, "dst", ".config-file")
		writeFile(t, src, 0o600, "content")

		if err := Copy(src, dst); err != nil {
			t.Fatalf("Copy: %v", err)
		}
		expectCleanedPath(t, *calls, dst)
	})

	t.Run("link strips the target", func(t *testing.T) {
		calls := recordCleanups(t)
		root := t.TempDir()
		target := filepath.Join(root, "storage", ".config-file")
		linkPath := filepath.Join(root, "home", ".config-file")
		writeFile(t, target, 0o600, "content")

		if err := Link(target, linkPath); err != nil {
			t.Fatalf("Link: %v", err)
		}
		expectCleanedPath(t, *calls, target)
	})
}

// expectCleanedPath fails unless every recorded cleanup command was aimed at
// the given path.
//
// Every one of them, not merely one: the path is the last argument of each
// command, and a primitive that cleaned the wrong path -- the source rather
// than the destination, the link rather than its target -- would still produce
// calls for a case that only counted them.
func expectCleanedPath(t *testing.T, calls []cleanupCall, path string) {
	t.Helper()
	if len(calls) == 0 {
		t.Fatalf("no cleanup command was asked for; want them aimed at %s", path)
	}
	for i, call := range calls {
		if got := call.Argv[len(call.Argv)-1]; got != path {
			t.Errorf("cleanup %d was aimed at %s, want %s", i, got, path)
		}
	}
}
