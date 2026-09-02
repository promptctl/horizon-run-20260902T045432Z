package syncfs

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// Helpers shared by every case in the package. They all take exact modes and
// set them with an explicit chmod after the write, because os.WriteFile and
// os.MkdirAll apply the process umask: a fixture written "0644" on a machine
// with umask 077 arrives as 0600 and a case about clamping a permissive file
// then asserts nothing.

// writeFile writes a regular file with exactly the mode given, creating its
// parents.
func writeFile(t *testing.T, path string, mode fs.FileMode, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("preparing the parent of %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("setting the mode of %s: %v", path, err)
	}
}

// mustMkdir creates a directory with exactly the mode given, creating its
// parents.
func mustMkdir(t *testing.T, path string, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(path, mode); err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("setting the mode of %s: %v", path, err)
	}
}

// symlink creates linkPath pointing at target.
func symlink(t *testing.T, target, linkPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o700); err != nil {
		t.Fatalf("preparing the parent of %s: %v", linkPath, err)
	}
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatalf("linking %s to %s: %v", linkPath, target, err)
	}
}

// permOf reports a path's permission bits, following symlinks the way a chmod
// does.
func permOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

// contentOf reports a regular file's contents.
func contentOf(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(content)
}

// expectPerm fails unless a path carries exactly the mode given.
func expectPerm(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	if got := permOf(t, path); got != want {
		t.Errorf("%s has mode %04o, want %04o", path, got, want)
	}
}

// TestACopiedFileLandsMode0600 pins the first half of appspec/06 "Permissions
// (clamped on every write)" for the copy primitive: whatever the source's mode
// was, the destination is owner read+write only.
//
// The source is 0644 -- a perfectly ordinary mode for a config file -- so the
// case fails against a copy that preserves the source mode as well as against
// one that applies no mode at all.
func TestACopiedFileLandsMode0600(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src", ".config-file")
	dst := filepath.Join(root, "dst", ".config-file")
	writeFile(t, src, 0o644, "content")

	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	expectPerm(t, dst, 0o600)
	if got := contentOf(t, dst); got != "content" {
		t.Fatalf("copied contents = %q, want %q", got, "content")
	}
	// The source is untouched, which is what makes backup non-destructive to
	// home and restore non-destructive to storage (appspec/01 section 2,
	// "restore is the read-only inverse of backup").
	expectPerm(t, src, 0o644)
}

// TestAnExistingDestinationFileIsClampedTooNotJustANewOne pins that the clamp
// is a post-condition of the primitive rather than a flag on the file's
// creation.
//
// A copy over an existing destination is the common case: it is what every
// confirmed drift prompt leads to. A create-time mode alone would leave that
// file at whatever mode it already had, and the case that only ever copies to a
// fresh path cannot tell the difference.
func TestAnExistingDestinationFileIsClampedTooNotJustANewOne(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src", ".config-file")
	dst := filepath.Join(root, "dst", ".config-file")
	writeFile(t, src, 0o600, "new")
	writeFile(t, dst, 0o666, "old")

	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	expectPerm(t, dst, 0o600)
	if got := contentOf(t, dst); got != "new" {
		t.Fatalf("copied contents = %q, want %q", got, "new")
	}
}

// TestACopiedDirectoryTreeIsClamped0700And0600Recursively pins the other half
// of appspec/06 "Permissions": the mode "is set RECURSIVELY", directories to
// 0700 and regular files to 0600, at every depth rather than on the root of the
// copy alone.
//
// The tree is two levels deep for that reason. A clamp that chmods only the
// path it was handed, or only that path's immediate children, passes a
// one-level fixture.
func TestACopiedDirectoryTreeIsClamped0700And0600Recursively(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src", "app")
	dst := filepath.Join(root, "dst", "app")
	writeFile(t, filepath.Join(src, "top.conf"), 0o644, "top")
	writeFile(t, filepath.Join(src, "nested", "deep", "deep.conf"), 0o666, "deep")
	mustMkdir(t, filepath.Join(src, "nested", "deep"), 0o755)
	mustMkdir(t, filepath.Join(src, "nested"), 0o755)
	mustMkdir(t, src, 0o755)

	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	expectPerm(t, dst, 0o700)
	expectPerm(t, filepath.Join(dst, "nested"), 0o700)
	expectPerm(t, filepath.Join(dst, "nested", "deep"), 0o700)
	expectPerm(t, filepath.Join(dst, "top.conf"), 0o600)
	expectPerm(t, filepath.Join(dst, "nested", "deep", "deep.conf"), 0o600)
	if got := contentOf(t, filepath.Join(dst, "nested", "deep", "deep.conf")); got != "deep" {
		t.Fatalf("copied contents = %q, want %q", got, "deep")
	}
}

// TestCopyCreatesTheDestinationsMissingParentsRecursively pins appspec/06's
// first copy clause. The destination is three levels below anything that
// exists, which is the ordinary case for a config file nested inside an
// application's directory under a freshly created Mackup folder.
func TestCopyCreatesTheDestinationsMissingParentsRecursively(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src", ".config-file")
	dst := filepath.Join(root, "dst", "app", "deeper", ".config-file")
	writeFile(t, src, 0o600, "content")

	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if got := contentOf(t, dst); got != "content" {
		t.Fatalf("copied contents = %q, want %q", got, "content")
	}
}

// TestCopyDoesNotNarrowAnExistingParentDirectory pins the upward limit of the
// clamp: appspec/06 makes 0600/0700 a post-condition of the path that was
// copied, not of the directories above it.
//
// The parent is pre-created at 0755, so the case asserts an exact mode on every
// machine whatever its umask. A clamp that walked upward would narrow the
// Mackup folder itself on the first file copied into it -- a directory the user
// made and the program was never asked to manage.
//
// The other half of the parentMode claim -- the mode a parent this primitive
// CREATES ends up with -- cannot be asserted without fixing the umask, so it
// lives in TestCopyCreatesMissingParentsWithoutClampingThem next door.
func TestCopyDoesNotNarrowAnExistingParentDirectory(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src", ".config-file")
	parent := filepath.Join(root, "dst", "app")
	dst := filepath.Join(parent, ".config-file")
	writeFile(t, src, 0o600, "content")
	mustMkdir(t, parent, 0o755)

	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	expectPerm(t, dst, 0o600)
	expectPerm(t, parent, 0o755)
}

// TestCopyMergesIntoAnExistingDestinationDirectory pins both halves of
// appspec/06's merge rule at once: "existing destination files are overwritten
// by same-named source files, destination-only files are left".
//
// The second half is the one an implementation loses by clearing the
// destination first, and it is not cosmetic -- it is what lets a storage folder
// hold files this machine's copy of an application does not have.
func TestCopyMergesIntoAnExistingDestinationDirectory(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src", "app")
	dst := filepath.Join(root, "dst", "app")
	writeFile(t, filepath.Join(src, "shared.conf"), 0o600, "from source")
	writeFile(t, filepath.Join(src, "only-source.conf"), 0o600, "source only")
	writeFile(t, filepath.Join(dst, "shared.conf"), 0o600, "from destination")
	writeFile(t, filepath.Join(dst, "only-destination.conf"), 0o600, "destination only")

	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if got := contentOf(t, filepath.Join(dst, "shared.conf")); got != "from source" {
		t.Errorf("the same-named destination file holds %q, want it overwritten by %q", got, "from source")
	}
	if got := contentOf(t, filepath.Join(dst, "only-source.conf")); got != "source only" {
		t.Errorf("the source-only file holds %q", got)
	}
	if got := contentOf(t, filepath.Join(dst, "only-destination.conf")); got != "destination only" {
		t.Errorf("the destination-only file holds %q, want it left alone", got)
	}
}

// TestCopyFollowsASymlinkedSourceAndWritesARealFile pins the classification
// being made with a stat that follows links.
//
// It is what `link uninstall` needs -- appspec/06 has it copy the mackup copy
// into home "as a real file" -- and it is the reason backup needs its link-skip
// at all: without following, a symlinked home path would be reproduced as a
// link in storage rather than skipped or copied.
func TestCopyFollowsASymlinkedSourceAndWritesARealFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "src", "real.conf")
	src := filepath.Join(root, "src", "link.conf")
	dst := filepath.Join(root, "dst", "copy.conf")
	writeFile(t, target, 0o644, "content")
	symlink(t, target, src)

	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("lstat %s: %v", dst, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		t.Fatalf("%s was reproduced as a symlink, want a real file", dst)
	}
	if got := contentOf(t, dst); got != "content" {
		t.Fatalf("copied contents = %q, want %q", got, "content")
	}
	expectPerm(t, dst, 0o600)
}

// TestCopyDescendsIntoASymlinkedDirectoryInsideTheSourceTree pins the
// deliberate opposite of TestClampDoesNotDescendThroughASymlinkedDirectory, so
// that the asymmetry between the two walks is a decision on the record.
//
// The contents arrive as real files and the link is not reproduced, which is
// what makes a storage folder portable: a link copied as a link points at a
// path that exists on the machine that made it and nowhere else. It is also
// what the reference does -- shutil.copytree's default symlinks=False follows
// and copies contents, verified against the library rather than recalled.
//
// The consequence is asserted rather than only permitted, because it is the
// surprising one: a config directory holding a link to a large tree elsewhere
// duplicates that tree into storage. That is the reference's behavior and the
// application database's decision about which paths to manage, not this
// primitive's to override -- but a reader meeting it for the first time should
// find it pinned rather than inferred.
func TestCopyDescendsIntoASymlinkedDirectoryInsideTheSourceTree(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src", "app")
	dst := filepath.Join(root, "dst", "app")
	outside := filepath.Join(root, "outside")
	writeFile(t, filepath.Join(outside, "inside.conf"), 0o644, "linked content")
	writeFile(t, filepath.Join(src, "plain.conf"), 0o644, "plain content")
	symlink(t, outside, filepath.Join(src, "linkdir"))

	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	copied := filepath.Join(dst, "linkdir")
	info, err := os.Lstat(copied)
	if err != nil {
		t.Fatalf("lstat %s: %v", copied, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		t.Fatalf("%s was reproduced as a symlink, want a real directory", copied)
	}
	if got := contentOf(t, filepath.Join(copied, "inside.conf")); got != "linked content" {
		t.Fatalf("the linked directory's contents were not copied; got %q", got)
	}
	// And the clamp reached through the descent, since the copied entries are
	// ordinary files and directories to it by the time it walks them.
	expectPerm(t, copied, 0o700)
	expectPerm(t, filepath.Join(copied, "inside.conf"), 0o600)
	// The source side is untouched: descending copies, it does not move.
	expectPerm(t, filepath.Join(outside, "inside.conf"), 0o644)
}

// TestCopyingSomethingThatIsNeitherAFileNorADirectoryIsAnError pins appspec/06's
// fourth copy clause, and pins it for an entry nested inside a source tree as
// well as for the path handed in.
//
// The nested arm is the one worth having: a walk that classified only the root
// and then copied blindly beneath it would pass the first arm and silently
// produce garbage for the second.
func TestCopyingSomethingThatIsNeitherAFileNorADirectoryIsAnError(t *testing.T) {
	t.Run("the source itself", func(t *testing.T) {
		root := t.TempDir()
		src := filepath.Join(root, "src", "dangling")
		symlink(t, filepath.Join(root, "src", "gone"), src)

		if err := Copy(src, filepath.Join(root, "dst", "x")); err == nil {
			t.Fatalf("Copy of a dangling symlink succeeded, want an error")
		}
	})
	t.Run("an entry inside the source tree", func(t *testing.T) {
		root := t.TempDir()
		src := filepath.Join(root, "src", "app")
		writeFile(t, filepath.Join(src, "ok.conf"), 0o600, "content")
		symlink(t, filepath.Join(src, "gone"), filepath.Join(src, "dangling"))

		if err := Copy(src, filepath.Join(root, "dst", "app")); err == nil {
			t.Fatalf("Copy of a tree holding a dangling symlink succeeded, want an error")
		}
	})
	t.Run("a source that does not exist", func(t *testing.T) {
		root := t.TempDir()
		if err := Copy(filepath.Join(root, "gone"), filepath.Join(root, "dst")); err == nil {
			t.Fatalf("Copy of an absent source succeeded, want an error")
		}
	})
}

// TestDeleteRemovesASymlinkAndNotItsTarget pins appspec/06's "a symlink is
// removed as the link, not its target".
//
// This is the single most destructive thing this package could get wrong. Every
// `link uninstall` deletes a home symlink whose target is the storage copy, and
// a delete that followed the link would remove the user's only remaining copy
// of the file -- while appspec/01 states as a whole-program invariant that "no
// transition ever deletes the storage copy".
//
// Both kinds of link are covered, because they take different arms of the
// implementation: a symlink to a directory is the one that an IsDir test asked
// of a following stat would send to a recursive removal.
func TestDeleteRemovesASymlinkAndNotItsTarget(t *testing.T) {
	t.Run("a link to a file", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "storage", "real.conf")
		link := filepath.Join(root, "home", "link.conf")
		writeFile(t, target, 0o600, "content")
		symlink(t, target, link)

		if err := Delete(link); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := os.Lstat(link); !os.IsNotExist(err) {
			t.Errorf("the link survived the delete: %v", err)
		}
		if got := contentOf(t, target); got != "content" {
			t.Errorf("the link's target holds %q", got)
		}
	})
	t.Run("a link to a directory", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "storage", "app")
		link := filepath.Join(root, "home", "app")
		writeFile(t, filepath.Join(target, "inside.conf"), 0o600, "content")
		symlink(t, target, link)

		if err := Delete(link); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := os.Lstat(link); !os.IsNotExist(err) {
			t.Errorf("the link survived the delete: %v", err)
		}
		if got := contentOf(t, filepath.Join(target, "inside.conf")); got != "content" {
			t.Errorf("the linked directory's contents were removed; the file holds %q", got)
		}
	})
}

// TestDeleteRemovesADirectoryRecursively pins the other half of appspec/06's
// delete clause.
func TestDeleteRemovesADirectoryRecursively(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "app")
	writeFile(t, filepath.Join(dir, "nested", "deep.conf"), 0o600, "content")

	if err := Delete(dir); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("the directory survived the delete: %v", err)
	}
}

// TestDeletingAPathThatIsNotThereSucceeds pins the silence the reference has
// when none of its three kind-tests matches.
//
// It is what makes a run re-entered after an interruption converge: appspec/07
// puts the dangerous window between `link install`'s delete and its symlink,
// and appspec/01 says the recovery "is simply re-running". A delete that
// reported an absent path as a failure would turn that recovery into a
// per-file error on every interrupted file.
func TestDeletingAPathThatIsNotThereSucceeds(t *testing.T) {
	root := t.TempDir()
	if err := Delete(filepath.Join(root, "never", "existed")); err != nil {
		t.Fatalf("Delete of an absent path: %v", err)
	}
}

// TestLinkPointsAtTheTargetItWasGivenAndCreatesMissingParents pins the two
// mechanical clauses of appspec/06 "link(target, link_path)".
//
// The link's recorded target is read with os.Readlink rather than checked by
// resolving it, because appspec/06 says the target is an absolute path and a
// caller passing a relative one must not silently appear to work: resolution
// would succeed whenever the test happened to run with the link's directory as
// the working directory.
func TestLinkPointsAtTheTargetItWasGivenAndCreatesMissingParents(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "storage", "Mackup", "app", ".config-file")
	linkPath := filepath.Join(root, "home", "app", "deeper", ".config-file")
	writeFile(t, target, 0o600, "content")

	if err := Link(target, linkPath); err != nil {
		t.Fatalf("Link: %v", err)
	}
	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink %s: %v", linkPath, err)
	}
	if got != target {
		t.Fatalf("the link records %q, want %q", got, target)
	}
	if content := contentOf(t, linkPath); content != "content" {
		t.Fatalf("reading through the link gave %q", content)
	}
}

// TestLinkClampsItsTargetBeforeCreatingTheLink pins the ORDER appspec/06 states
// -- "Before linking, target's permissions are set recursively" -- and not
// merely that both happen.
//
// Order is unobservable on a run where both steps succeed, since the end state
// is the same either way. It is made observable by arranging for the symlink to
// fail: something already occupies the link path, so os.Symlink returns EEXIST.
// A clamp that ran BEFORE has already narrowed the target; a clamp that ran
// after never runs at all. The failing run is the whole point of the case, so
// the error is required rather than tolerated.
//
// This is not a contrived state. It is exactly what `link` meets on a second
// machine whose home already holds the file, before the prompt-and-delete of
// appspec/06's step 3 -- and a link operation that fails mid-run is the
// documented behavior of the whole link family (appspec/01 section 5).
func TestLinkClampsItsTargetBeforeCreatingTheLink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "storage", "app")
	linkPath := filepath.Join(root, "home", "app")
	writeFile(t, filepath.Join(target, "nested", "deep.conf"), 0o644, "content")
	mustMkdir(t, filepath.Join(target, "nested"), 0o755)
	mustMkdir(t, target, 0o755)
	// Occupies the link path, so the symlink cannot be created.
	writeFile(t, linkPath, 0o600, "in the way")

	if err := Link(target, linkPath); err == nil {
		t.Fatalf("Link over an occupied path succeeded; the case cannot observe the order")
	}
	expectPerm(t, target, 0o700)
	expectPerm(t, filepath.Join(target, "nested"), 0o700)
	expectPerm(t, filepath.Join(target, "nested", "deep.conf"), 0o600)
}

// TestClampSkipsABrokenSymlinkInsteadOfFailing pins appspec/06's exception:
// "Broken symlinks encountered while walking a directory are skipped (not
// chmod-ed) rather than causing failure."
//
// The tree holds a real file beside the dangling link, and its mode is checked
// too: a walk that aborted at the link would leave the rest of the tree
// unclamped, and a case asserting only that Clamp returned nil would not see
// it. The order os.ReadDir returns entries in is sorted, so "dangling" is
// reached before "later.conf" and the abort would be visible.
func TestClampSkipsABrokenSymlinkInsteadOfFailing(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "app")
	writeFile(t, filepath.Join(dir, "later.conf"), 0o644, "content")
	symlink(t, filepath.Join(dir, "gone"), filepath.Join(dir, "dangling"))

	if err := Clamp(dir); err != nil {
		t.Fatalf("Clamp over a tree holding a dangling symlink: %v", err)
	}
	expectPerm(t, dir, 0o700)
	expectPerm(t, filepath.Join(dir, "later.conf"), 0o600)
	// The link itself is still there, and still dangling: skipped means
	// skipped, not repaired and not removed.
	if _, err := os.Lstat(filepath.Join(dir, "dangling")); err != nil {
		t.Fatalf("the dangling link did not survive the clamp: %v", err)
	}
}

// TestClampDoesNotDescendThroughASymlinkedDirectory pins the limit of the
// recursion, and the surprising half of it that clampTree's comment records.
//
// What does NOT happen is the recursion: the file two levels out, reachable
// only through the link, keeps its own mode. A walk that followed links would
// narrow an arbitrary part of the home directory that merely happened to be
// reachable from a config directory.
//
// What DOES happen is that the link's own target is chmod-ed, directory or
// file, because a chmod follows the link and appspec/06 excuses only BROKEN
// symlinks from the walk -- "broken symlinks ... are skipped (not chmod-ed)
// RATHER THAN CAUSING FAILURE", which is the rationale for that one kind and
// not for links in general. Both are asserted here so that the behavior is a
// decision on the record rather than a side effect nobody looked at: a
// reimplementer reading only the code could reasonably conclude either way, and
// the difference shows up on a user who put a symlink inside their storage
// folder by hand.
func TestClampDoesNotDescendThroughASymlinkedDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "app")
	outside := filepath.Join(root, "outside")
	writeFile(t, filepath.Join(outside, "untouched.conf"), 0o644, "content")
	writeFile(t, filepath.Join(root, "sibling.conf"), 0o644, "content")
	mustMkdir(t, outside, 0o755)
	mustMkdir(t, dir, 0o755)
	symlink(t, outside, filepath.Join(dir, "away"))
	symlink(t, filepath.Join(root, "sibling.conf"), filepath.Join(dir, "sibling"))

	if err := Clamp(dir); err != nil {
		t.Fatalf("Clamp: %v", err)
	}
	expectPerm(t, dir, 0o700)
	// The two link targets: chmod-ed, because the chmod followed the link.
	expectPerm(t, outside, 0o700)
	expectPerm(t, filepath.Join(root, "sibling.conf"), 0o600)
	// And the file only reachable THROUGH the linked directory: untouched,
	// because the walk did not descend.
	expectPerm(t, filepath.Join(outside, "untouched.conf"), 0o644)
}

// TestClampRefusesAPathThatIsNeitherAFileNorADirectory pins the strict
// classification of the root, which is the asymmetry Clamp's comment records
// against the permissive treatment of entries inside a walked tree.
func TestClampRefusesAPathThatIsNeitherAFileNorADirectory(t *testing.T) {
	root := t.TempDir()
	if err := Clamp(filepath.Join(root, "gone")); err == nil {
		t.Errorf("Clamp of an absent path succeeded, want an error")
	}
	dangling := filepath.Join(root, "dangling")
	symlink(t, filepath.Join(root, "gone"), dangling)
	if err := Clamp(dangling); err == nil {
		t.Errorf("Clamp of a dangling symlink succeeded, want an error")
	}
}
