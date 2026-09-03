package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/promptctl/macklebox/internal/fault"
)

// Levels 2 and 3 of the environment-gate lattice of appspec/01-architecture.md
// section 4 -- the ONLY per-command branch in the startup pipeline.
//
//	2. Backup-usable  -- usable environment, then ENSURE the Mackup folder
//	                     exists (create-on-confirm). backup, link install.
//	3. Restore-usable -- usable environment, then REQUIRE it already exists
//	                     (fatal if absent). restore, link, link uninstall.
//
// They live here rather than in stages.go with level 1, and that placement is
// contract rather than filing. appspec/06 "Environment gate per command" says
// that when an <application> is named "its validity is checked BEFORE this
// gate, so an unknown app name fails with `Unsupported application: <name>`
// (exit 1) before any folder is created or prompt shown". Level 1 runs for
// every command before dispatch; these two run inside the command, after the
// name has been validated. Hoisting them next to level 1 would make
// `backup frobnicate` prompt the user to create a directory and then reject
// the argument that made the run pointless.
//
// appspec/01 section 4 also states what these are the sole authority for: "the
// gate is the SOLE place the Mackup folder is created". Nothing else in the
// program calls MkdirAll on it -- syncfs creates the ancestors of a
// destination on the way to a file, which is a different act on a different
// path, and it deliberately does not clamp them.

// createFolderQuestion is the prompt of appspec/06: "Mackup needs a directory
// to store your configuration files / Do you want to create it now? <path>".
// The slash in the specification joins two lines; this is those two lines,
// with the path interpolated into the second.
const createFolderQuestion = "Mackup needs a directory to store your configuration files\n" +
	"Do you want to create it now? %s"

// ensureMackupFolder is level 2: the folder exists when this returns nil,
// having been created on confirmation if it was absent.
//
// Not suppressed by --dry-run, which is the one exception appspec/01 section 3
// carves out of the dry-run rule and states in as many words: "dry-run does not
// suppress the startup 'create the storage sub-folder' decision for backup /
// link install -- that gate runs before the per-file loop and, under a force
// flag, will still create the folder; absent a force flag under dry-run it
// will still prompt. Stated as: dry-run = no per-file mutation, applied by the
// executor uniformly; environment gates are not per-file mutations and run
// regardless." So this function is not given the dry-run flag at all -- there
// is nothing here for it to decide, and a parameter it must ignore is an
// invitation to stop ignoring it.
//
// The folder is created with the process umask applied to 0777, NOT with the
// 0700 of appspec/06's clamp. The clamp is a post-condition of the copy and
// link primitives on the path they were asked to act on; the Mackup folder is
// a directory the program creates to hold that path, and internal/syncfs makes
// the same distinction for the ancestors it creates, for the same stated
// reason: narrowing a directory the program was never asked to manage is a
// side effect on the user's storage, not a permission this program clamps.
func ensureMackupFolder(confirm confirmer, folder string) error {
	present, err := folderPresent(folder)
	if err != nil {
		return err
	}
	if present {
		return nil
	}
	create, err := confirm.Ask(fmt.Sprintf(createFolderQuestion, folder))
	if err != nil {
		return err
	}
	if !create {
		// appspec/07's error table, verbatim in its shape column: "Declined
		// 'create Mackup folder' prompt -> Error: Mackup can't do anything
		// without a home =(", guarded, exit 1.
		return fault.Guardedf("Mackup can't do anything without a home =(")
	}
	if err := os.MkdirAll(folder, 0o777); err != nil {
		// Guarded, not unguarded: the user answered the question the program
		// asked and the program could not do what it promised. appspec/07's
		// unguarded rows are all inputs the reference never anticipated; a
		// read-only storage root is a condition this gate exists to meet.
		return fault.Guardedf("Unable to create the Mackup folder: %s", err)
	}
	return nil
}

// requireMackupFolder is level 3: the folder must already exist.
//
// appspec/07's error table gives the failure as "Error: Unable to find the
// Mackup folder: <path>" plus a hint, guarded, exit 1. The hint is the second
// line: it is what separates this from level 1's storage-root failure for a
// user who has run neither command, since the answer -- back up on the machine
// that has the files, or let the sync client bring the folder down -- is not
// derivable from the missing path alone.
//
// It creates nothing, which is the entire difference between the two levels and
// the reason restore cannot bring a machine up from nothing: there would be no
// content to restore, and a fresh empty folder would make an empty restore look
// like a successful one.
func requireMackupFolder(folder string) error {
	present, err := folderPresent(folder)
	if err != nil {
		return err
	}
	if present {
		return nil
	}
	return fault.Guardedf("Unable to find the Mackup folder: %s\n"+
		"Run `mackup backup` on a machine that has your files, or let your sync "+
		"client bring the folder down, before restoring or linking.", folder)
}

// folderPresent reports whether the Mackup folder is a directory that is there,
// and separates that question from the one both gates used to conflate it with:
// whether the program could look at all.
//
// A directory, not merely something that exists, for the reason level 1's
// storage-root check gives: appspec/06 puts the synced files INSIDE this path,
// and a regular file sitting where the Mackup folder belongs cannot hold them.
// Reporting it as present would move the failure to the first copy, where it
// arrives as a per-file "Unable to copy" line for every file in the run rather
// than as the one gate failure it is. A file there is therefore absent, and
// that stays true.
//
// Stat and not Lstat: pointing the Mackup folder at another volume through a
// symlink is an ordinary arrangement for a directory that lives in a sync
// client's tree, and the directory it resolves to is the one the files are
// written into.
//
// A stat that fails for any reason other than ENOENT establishes NEITHER
// answer, and returning false for it made both gates assert something the
// program had not found out. A storage root without its search bit is enough:
// level 1 stats the root itself and passes, then this stat gets EACCES, and
// restore reported "Unable to find the Mackup folder" for a folder sitting
// right there -- with a hint sending the user to re-run backup on another
// machine rather than at the permission. Backup, on the same input, offered to
// CREATE the folder that already exists.
//
// This is a deliberate divergence from the reference, which reaches this
// through Python's os.path.isdir and so answers false for every stat error
// alike; appspec/07's table has a row for a MISSING folder and none for an
// unreadable one. appspec/01 section 5 is the licence -- a reimplementation may
// fix a defect of the reference "without changing any successful-run behavior"
// -- and this changes none: the run exits 1 either way, and only the diagnostic
// moves, from a claim to what actually happened.
func folderPresent(folder string) (bool, error) {
	info, err := os.Stat(folder)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fault.Guardedf("Unable to inspect the Mackup folder: %s", err)
	}
	return info.IsDir(), nil
}
