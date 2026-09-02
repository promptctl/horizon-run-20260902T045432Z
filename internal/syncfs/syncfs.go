// Package syncfs is the shared vocabulary of appspec/06-sync-operations.md: the
// file operations every sync command is built out of, the recursive permission
// clamp that is a post-condition of two of them, and the per-file state model
// of appspec/01-architecture.md section 2.
//
// appspec/01 draws the program as "four resolvers feeding one uniform per-file
// executor" and says the five sync commands "are five leaves on one tree, not
// five independent programs". This package is the trunk. It holds copy, delete
// and link with the exact post-conditions appspec/06 gives them, the
// 0600/0700 clamp that appspec/06 calls "a post-condition of BOTH the copy
// primitive and the link primitive, so it holds for all five operations", and
// the already-linked predicate that appspec/01 requires be defined once because
// four operations consult it and "a reimplementer who codes this check four
// times risks four subtly different answers".
//
// What it deliberately does not hold: the procedures. Which files are visited,
// in what order, what is printed, what is prompted and what a declined prompt
// means are appspec/06's per-command sections, and they belong to the commands.
// This package moves bytes and reports what it found; it reads no
// configuration, consults no application database, writes nothing to any
// stream, and asks the user nothing.
//
// Errors from here are plain errors, not internal/fault values. fault carries
// the two STARTUP regimes of appspec/01 section 6, and a per-file copy failure
// is in neither: appspec/06's partial-failure contract gives it a third
// treatment -- "recorded as data", rendered by the caller as "Error: Unable to
// copy <src> to <dst>: <reason>" and aggregated at end of run. The reason is
// what these functions return; the sentence around it is the executor's.
//
// # A feature this package deliberately does not have
//
// appspec/06 records under "Verified absent" that the reference carries a
// pgrep-based "is application X running?" check whose one call site is
// disabled, and that "a reimplementer should not infer such a feature". There
// is no such check here and no process is consulted before a file is touched.
// The only subprocesses this package spawns are the four attribute-cleanup
// commands in attributes.go, which appspec/06 calls "the only subprocesses the
// program spawns during sync".
package syncfs

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// The modes appspec/06 "Permissions (clamped on every write)" fixes, and the
// mode this package creates a destination's missing ancestors with.
const (
	// fileMode is "owner read+write only" -- every regular file this program
	// writes ends up here, whatever it was before.
	fileMode = 0o600
	// dirMode is "owner read+write+execute only".
	dirMode = 0o700
	// parentMode is what the ancestors of a destination are created with, and
	// it is deliberately NOT dirMode. appspec/06 makes the clamp a
	// post-condition of the path that was copied or linked, not of the
	// directories created above it to reach that path, and the reference
	// creates them with the process umask applied to 0777. Clamping them too
	// would make backup narrow the permissions of a directory it was never
	// asked to manage -- the Mackup folder itself, on the first file copied
	// into a fresh one.
	parentMode = 0o777
)

// Copy copies src to dst with the semantics of appspec/06 "copy(src, dst)".
//
// The four clauses of that section, in order: the parent of dst is created
// recursively if missing; a regular file is copied as a file and a directory
// recursively, merging into an existing destination; permissions are set
// recursively afterwards; and copying something that is neither is an error.
//
// Symlinks are followed, not reproduced. os.Stat and not os.Lstat is what makes
// a symlinked source copy as the real file it points at, which is the behavior
// the rest of the specification is written around: `link uninstall` copies the
// mackup copy "into the home path (as a real file)", and backup's link-skip
// exists precisely BECAUSE a home symlink into storage would otherwise be
// copied back over its own target as content.
//
// A source that does not exist and a source that is a socket, device or FIFO
// take the same arm and get the same error. That is not laziness about the
// distinction: the reference classifies with an is-file / is-directory pair
// that is false for both, so both are one "unsupported" failure there, and the
// callers have already established existence -- appspec/06's per-file step 1
// skips a source that "does not exist as a regular file or directory" before
// any copy is attempted.
func Copy(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), parentMode); err != nil {
		return err
	}
	info, err := os.Stat(src)
	switch {
	case err == nil && info.Mode().IsRegular():
		err = copyFile(src, dst)
	case err == nil && info.IsDir():
		err = copyTree(src, dst)
	default:
		err = unsupported(src)
	}
	if err != nil {
		return err
	}
	return Clamp(dst)
}

// unsupported is the error appspec/06 gives a copy of something that is
// "neither a regular file nor a directory". One constructor, because Copy
// raises it for the root and copyTree for an entry inside a tree, and a reader
// comparing two spellings of the same refusal cannot tell whether the
// difference is meaningful.
func unsupported(path string) error {
	return fmt.Errorf("%s is not a regular file or directory", path)
}

// copyFile copies one regular file's contents, truncating an existing
// destination.
//
// The destination is created with fileMode, so a file that did not exist is
// never briefly readable by anyone else on its way to the clamp. An existing
// destination keeps whatever mode it had until Copy's clamp reaches it, which
// is the reference's behavior and is why the clamp is a post-condition of the
// whole primitive rather than a flag on the create.
func copyFile(src, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	destination, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fileMode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destination, source); err != nil {
		destination.Close()
		return err
	}
	return destination.Close()
}

// copyTree copies a directory recursively, merging into an existing
// destination.
//
// appspec/06's merge rule has two halves and this function is only correct
// because of what it does NOT do: "existing destination files are overwritten
// by same-named source files" is copyFile truncating, and "destination-only
// files are left" is the absence of any delete here. A tempting `os.RemoveAll`
// on the destination first would make a directory copy destructive in a way no
// section of the specification describes, and would break the merge that lets a
// second machine's storage folder accumulate files the first one does not have.
//
// Entries are classified with a following stat, so a symlink to a directory is
// DESCENDED INTO and its contents written as real files. That is the opposite
// of what clampTree does with the same kind of entry, and the asymmetry is a
// decision rather than an oversight: the two walks answer different questions.
// Copy is about content, and reproducing a link inside the storage folder would
// leave the second machine pointing at a path that exists only on the first --
// which is the portability the storage folder is for. Clamp is about
// permissions, where descending would mutate files outside the tree the program
// was asked to manage. Both match the reference: shutil.copytree's default
// symlinks=False follows and copies contents, verified rather than recalled,
// and its walk does not skip live links either.
//
// The cost is the reference's too, and is left as the reference leaves it. A
// tree holding a directory symlink is copied with that subtree duplicated into
// storage, and a self-referential one recurses on a growing path until the
// operating system returns ELOOP. Neither is silent: the second surfaces as an
// ordinary per-file copy failure, which appspec/06's partial-failure contract
// records as data and reports in the end-of-run summary, failing that file
// rather than the run.
func copyTree(src, dst string) error {
	if err := os.MkdirAll(dst, dirMode); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		from := filepath.Join(src, entry.Name())
		to := filepath.Join(dst, entry.Name())
		info, statErr := os.Stat(from)
		var err error
		switch {
		case statErr == nil && info.Mode().IsRegular():
			err = copyFile(from, to)
		case statErr == nil && info.IsDir():
			err = copyTree(from, to)
		default:
			err = unsupported(from)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Delete removes path with the semantics of appspec/06 "delete(path)".
//
// Attributes are stripped first -- that is the section's first clause, and the
// reason it is first is that an immutable flag or a restrictive ACL is exactly
// what would make the removal below fail.
//
// A symlink is removed as the link and not as its target -- the difference
// between `link uninstall` reverting a link and `link uninstall` destroying the
// storage copy the link points at, which appspec/01 forbids outright ("no
// transition ever deletes the storage copy").
//
// The kind is asked of the Lstat result, and the reason recorded here is a
// weaker one than it first appears, so it is written as what it is. Both
// removals below are already link-safe: os.Remove unlinks a symlink rather than
// following it, and os.RemoveAll begins with an os.Remove that succeeds on one,
// so even an os.Stat here would leave the target intact. Verified, on a symlink
// to a non-empty directory: the link goes, the directory and its contents stay.
// What the Lstat buys is that the safety is stated rather than inherited from
// two standard-library functions that could reasonably have been written to
// follow, and that the three-way test matches the reference's order, which asks
// is-link before is-directory for exactly this reason.
//
// Deleting a path that is not there succeeds and does nothing. The reference
// tests for each of the three kinds and falls off the end when none matches,
// and that silence is load-bearing rather than incidental: every command's
// delete is followed by a copy or a link that recreates the path, so a run
// re-entered after an interruption between the two (appspec/07 "Interruption /
// crash residue") finds the delete already done and must be able to carry on.
func Delete(path string) error {
	cleanAttributes(path)

	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

// Link creates linkPath as a symbolic link pointing at target, with the
// semantics of appspec/06 "link(target, link_path)".
//
// Its three clauses in the order that section writes them: the parent of
// linkPath is created if missing, target's permissions are set recursively
// before linking, and the link is created. The clamp being BEFORE the symlink
// is the part worth stating, and it is not an ordering nicety: once linkPath
// exists, the home path is a live pointer into storage, and a target still
// carrying group- or world-readable modes is a config file exposed under the
// name the user believes is managed. appspec/06 makes it a post-condition of
// this primitive precisely so that no caller has to remember it.
//
// The target is written into the link as given, so a caller must pass the
// absolute mackup path appspec/06 specifies. A relative target would resolve
// against the link's own directory, which for a nested config file is not the
// home directory and not the Mackup folder either.
func Link(target, linkPath string) error {
	if err := os.MkdirAll(filepath.Dir(linkPath), parentMode); err != nil {
		return err
	}
	if err := Clamp(target); err != nil {
		return err
	}
	return os.Symlink(target, linkPath)
}

// Clamp sets path's mode recursively to the 0600/0700 of appspec/06
// "Permissions (clamped on every write)".
//
// It is exported because it is a named post-condition of two primitives rather
// than an internal step of one, and because appspec/01 records its user-visible
// consequence as a contract: after `link uninstall` copies a file back into
// home "the home file's mode is 0600 even if it was more permissive before
// being synced -- a round-tripped file may come back LESS permissive than the
// original". That is a promise about this function, and a reimplementation is
// entitled to observe it directly.
//
// Attributes are stripped first, for the reason Delete strips them: an
// immutable flag is what makes a chmod fail.
//
// The root of the walk is classified strictly -- a socket or a missing path is
// an error -- while entries found inside a directory are not, and the asymmetry
// is the reference's. Its recursive walk chmods everything it enumerates that
// is not a directory, so an oddity nested inside a config directory does not
// stop the operation, while an oddity handed in as the whole target is a caller
// mistake worth reporting.
func Clamp(path string) error {
	cleanAttributes(path)

	info, err := os.Stat(path)
	switch {
	case err == nil && info.Mode().IsRegular():
		return os.Chmod(path, fileMode)
	case err == nil && info.IsDir():
		if err := os.Chmod(path, dirMode); err != nil {
			return err
		}
		return clampTree(path)
	default:
		return unsupported(path)
	}
}

// clampTree applies the clamp to everything below an already-clamped directory.
//
// Broken symlinks are skipped rather than failing the walk, which appspec/06
// states outright: "Broken symlinks encountered while walking a directory are
// skipped (not chmod-ed) rather than causing failure." A chmod follows the
// link, so a dangling one is an ENOENT on a path that is plainly there -- the
// one condition in this walk that says nothing about whether the operation
// succeeded.
//
// The skip is decided on the entry's own type and the failure of a stat
// THROUGH it, not on the stat failure alone. A non-symlink entry that cannot be
// stat-ed is a genuine problem and is reported; treating every stat failure as
// a broken link would turn an unreadable directory into a silently
// partially-clamped tree, which is the shape of failure this clamp exists to
// prevent.
//
// The walk does not DESCEND through a symlinked directory, because it reads
// each entry's own type -- so a config directory holding a link to elsewhere in
// the home directory does not have that elsewhere recursively narrowed to 0700.
// The linked directory itself is still chmod-ed, like every other entry, since
// a chmod follows the link; only the recursion stops. That is the reference's
// non-following walk exactly, and the pair is worth stating together because
// the second half is the surprising one and a reader who assumed symlinks were
// skipped outright would be wrong about it.
func clampTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Clamp has already chmod-ed the root, and classified it under the
		// strict rule this callback deliberately does not apply.
		if path == root {
			return nil
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			if entry.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			return statErr
		}
		if info.IsDir() {
			return os.Chmod(path, dirMode)
		}
		return os.Chmod(path, fileMode)
	})
}
