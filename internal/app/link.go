package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/promptctl/macklebox/internal/fault"
	"github.com/promptctl/macklebox/internal/syncfs"
)

// link install: move home files into the Mackup folder, then symlink them back.
//
// This is the first machine's entry door to Strategy B of appspec/00-overview.md
// -- the command that INVERTS where the source of truth lives. After backup, the
// home file is still the real one and storage holds a copy (appspec/01 section 2,
// "Backed-up"); after this, storage holds the real file and the home path is a
// pointer to it ("Linked"). appspec/00 promise 1 is what that buys: the
// application reads and writes through the link and sees no change, so an edit on
// one machine appears on the other.
//
// It shares everything appspec/01 section 1 says the five sync commands share --
// the two-level sorted fan-out and the verbose header (executor.go), the scope
// selector (scope.go), the level-2 folder gate (folder.go), the one confirmation
// mechanism (confirm.go), and the primitives and the one predicate
// (internal/syncfs) -- and it does NOT share the per-file procedure, because
// appspec/06 gives it its own and appspec/00's "two entry doors" section warns
// against collapsing these commands into each other.
//
// Two things about the failure regime, because they are the opposite of the copy
// commands' and reading this file against sync.go will otherwise look like an
// inconsistency.
//
// FIRST: link install does not aggregate. appspec/01 section 5 states the
// asymmetry as contract -- "backup/restore degrade gracefully and report; link
// commands fail hard mid-run" -- and appspec/07's error table gives it a row of
// its own: "Failure inside a link operation | stderr | nonzero | uncaught error,
// run stops mid-way | unguarded". So there is no failed slice here and no
// end-of-run summary; the per-file procedure returns an error and the executor's
// fan-out stops on it. appspec/01 section 5 does permit a reimplementation to
// aggregate link failures instead, and that permission is declined: taking it
// would make the row above unobservable, and an unobservable row is one nothing
// can hold this program to.
//
// SECOND, and this is the one to read before "fixing" linkableSource: a home path
// this command cannot stat is SKIPPED, not failed. That is the reference's
// answer, it is the opposite of what sourcePresent's caller in sync.go does with
// the same condition, and it is deliberate. The reasoning is written out at
// linkableSource.

// linkInstallVerbs is appspec/06 step 2's progress line for this command, which
// is the one place a command needs two different words: "Linking <f> ..." in the
// short form, and "Backing up\n  <home>\n  to\n  <mackup> ..." in the verbose
// four-line form. The verbose word is backup's, and that is what appspec/06
// writes -- it is not a copy-paste slip to be tidied into "Linking".
var linkInstallVerbs = progressVerbs{short: "Linking", long: "Backing up"}

// linkInstallReplaceQuestion is appspec/07's "Replace an existing backup on link
// install" prompt, verbatim. It carries no --force hint: appspec/07 gives that
// parenthetical to backup's prompt alone.
const linkInstallReplaceQuestion = "A %s named %s already exists in the backup. " +
	"Are you sure that you want to replace it?"

// The three verbose skip traces of appspec/06 step 1, "keyed on the LinkState
// (already backed up / broken link / does not exist)".
//
// Each names the home path on its own line, which is what makes the trace worth
// printing at all: the short progress line elsewhere names a relative path, and
// the question a reader has at a skip is which absolute path was looked at.
// appspec/07 states outright that wording is not a machine-read interface
// (only the "Unsupported application:" prefix, the force-conflict line and the
// exit codes are), so these are written to say what happened rather than to
// match the reference byte for byte.
const (
	doingNothingLinked = "Doing nothing\n  %s\n  already linked by Mackup"
	doingNothingBroken = "Doing nothing\n  %s\n  is a broken link, you might want to fix it."
	doingNothingAbsent = "Doing nothing\n  %s\n  does not exist"
)

// A linkRun is one invocation of a link-strategy command.
//
// It is the executor and nothing else, which is the point: everything link
// install needs beyond appspec/06's per-file procedure is already shared, and a
// field here would be a fact about linking that backup and restore were somehow
// not entitled to. The two remaining link tickets add their procedures as
// methods on this same type.
type linkRun struct {
	*executor
}

// runLinkInstall is the whole of `link install`: gate, fan out, exit.
//
// The order is appspec/06 "Environment gate per command" and appspec/01 section
// 3: the application name is validated FIRST, so `link install frobnicate`
// reports the unsupported key without creating a folder or showing a prompt.
// appspec/06 names this command explicitly in the rule.
//
// The gate is level 2 of appspec/01 section 4's lattice -- ENSURE, the same one
// backup runs, because both write into the Mackup folder and appspec/01 section
// 4 lists "backup, link install" together against it. Not called through a
// direction record: that record is the copy operation's, and link install is not
// a direction of it.
//
// There is no report() at the end and no summary, for the reason the file header
// gives. A run that gets here has linked everything it was asked to link.
func runLinkInstall(p pipeline) int {
	keys, known := resolveScope(p)
	if !known {
		return ExitFailure
	}

	run := &linkRun{executor: newExecutor(p)}
	if err := ensureMackupFolder(run.confirm, run.folder); err != nil {
		return reportFatal(p.streams, err)
	}
	if err := run.fanOut(keys, run.install); err != nil {
		// Every way out of the loop lands here, and they are two: a failure
		// inside the operation (appspec/07's "Failure inside a link operation"
		// row) and end of input at a confirmation prompt (its "EOF at a
		// confirmation prompt" row). Both are unguarded, both stop the run
		// mid-way, and both leave earlier files transitioned and later files
		// untouched -- which appspec/01 section 5 states as the contract and
		// appspec/00 promise 3 makes recoverable by re-running.
		return reportFatal(p.streams, err)
	}
	return ExitOK
}

// install is appspec/06 "`link install` -- move home files into Mackup, then
// symlink them back", step for step. Its four numbered steps are the four blocks
// below.
func (r *linkRun) install(relative string) error {
	homePath := filepath.Join(r.home, relative)
	mackupPath := filepath.Join(r.folder, relative)

	// Step 1. "Act only if the home path exists as a regular file or directory
	// and is not already a symlink to its mackup path (the shared predicate).
	// Otherwise, verbose prints a 'Doing nothing ...' trace keyed on the
	// LinkState ... and nothing happens."
	//
	// syncfs.AlreadyLinked, not a symlink test written here. appspec/01 section
	// 2: "a reimplementer who codes this check four times risks four subtly
	// different answers; there must be one definition." This is the second of
	// its four call sites; backup's link-skip is the first.
	//
	// The predicate is asked first because it is the cheap, total one -- it
	// returns a bool and never an error, by contract -- and because it is what
	// makes this command idempotent: appspec/00 promise 3's fixed point is
	// exactly this arm.
	if syncfs.AlreadyLinked(homePath, mackupPath) || !linkableSource(homePath) {
		r.doingNothing(homePath, mackupPath)
		return nil
	}

	// Step 2. "Print progress (`Linking <f> ...`, or verbose `Backing up ...`).
	// If dry-run, stop here for this file."
	r.progress(linkInstallVerbs, relative, homePath, mackupPath)
	if r.dryRun {
		return nil
	}

	// Steps 3 and 4, which are one question -- "is a copy already at the mackup
	// path?" -- with the prompt on one side of it.
	//
	// os.Lstat, so a symlink AT the mackup path counts as present and is
	// prompted about as a "link". Following it would let the copy below write
	// through a link in storage to a file the user never named, and would take
	// the delete on the yes arm to that file rather than to the link.
	//
	// A mackup path that could not be INSPECTED is neither step 3 nor step 4,
	// and this is the destination guard sync.go's per-file procedure carries,
	// for a sharper version of its reason. Reading every error as "nothing is
	// there" sends the file to step 4, which copies home over that path --
	// syncfs.Copy does not require an absent destination -- and then DELETES
	// THE HOME FILE. The prompt this branch skips is the only thing standing
	// between an uninspectable storage copy and its silent replacement by a
	// file whose original is then removed. The failure regime is the link one,
	// so the run stops here rather than recording and carrying on.
	//
	// Unlike sync.go's, this guard sits AFTER the progress line and the dry-run
	// stop rather than above them, because appspec/06 numbers the steps that
	// way for this command and nothing forces a hoist. sync.go's hoist is a
	// stated divergence, driven by a contradiction that has no counterpart
	// here: appspec/01 section 5 makes "a partial backup/restore can never exit
	// 0" unconditional, so a copy run that skipped a file and exited 0 broke a
	// contract in the specification itself. A dry run reaches nothing below
	// this line and mutates nothing on any path, which is appspec/00 promise 5.
	existing, err := os.Lstat(mackupPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return linkFailure(err, "inspect %s", mackupPath)
	}
	if err == nil {
		// Step 3. "If a copy already exists at the mackup path: prompt ... On
		// yes: delete the mackup copy, copy home->mackup, delete the home file,
		// create a symlink. On no: do nothing for this file."
		replace, err := r.confirm.Ask(fmt.Sprintf(
			linkInstallReplaceQuestion, typeNoun(existing), mackupPath))
		if err != nil {
			return err
		}
		if !replace {
			// Not a failure. A declined prompt is a deliberate answer, and
			// appspec/00 promise 4 makes declining the DEFAULT behaviour of
			// the safety gate; counting it as a failure would make --force-no,
			// which appspec/07 defines as "pre-answers with no" over every
			// prompt, end the run instead of skipping everything.
			return nil
		}
		if err := syncfs.Delete(mackupPath); err != nil {
			return linkFailure(err, "replace %s", mackupPath)
		}
	}

	// Step 4, and the tail of step 3: "copy home->mackup, delete the home file,
	// create a symlink at the home path pointing to the mackup copy."
	return r.move(homePath, mackupPath)
}

// move is appspec/06's three-operation per-file sequence, in the order that
// section and appspec/01 section 2 fix: copy home->mackup, delete home, symlink
// home->mackup.
//
// The order is contract and the window between the second and third operation is
// the "dangerous non-atomic window" of appspec/01 section 2 and appspec/07
// "Interruption / crash residue": an interruption there leaves the home path
// missing while the content sits in storage (transient Storage-only). appspec/01
// says a reimplementation "may make the move+link atomic but must not change the
// observable end state of a successful run" -- so this reproduces the reference
// order rather than taking that permission, and the recovery is what appspec/00
// promise 3 says it is: re-running `link install` or `link` re-links from the
// surviving storage copy.
//
// Nothing here chmods. appspec/06 makes the 0600/0700 clamp a post-condition of
// BOTH syncfs.Copy and syncfs.Link, so the storage copy is clamped by the first
// call and again by the third before the link is created. A clamp at this call
// site would be a second implementation of one rule.
func (r *linkRun) move(homePath, mackupPath string) error {
	if err := syncfs.Copy(homePath, mackupPath); err != nil {
		return linkFailure(err, "copy %s to %s", homePath, mackupPath)
	}
	if err := syncfs.Delete(homePath); err != nil {
		return linkFailure(err, "remove %s", homePath)
	}
	if err := syncfs.Link(mackupPath, homePath); err != nil {
		return linkFailure(err, "link %s to %s", homePath, mackupPath)
	}
	return nil
}

// doingNothing prints appspec/06 step 1's verbose trace for a file this command
// left alone, keyed on the LinkState.
//
// syncfs.StateOf and not a second reading of the filesystem here: appspec/06
// says of the state model "model it once as a type rather than re-deriving it in
// each operation", and this is the trace that names the state, so it must be the
// state the model reports. Its already-linked arm and step 1's guard are
// therefore the same predicate, which is what stops the program from skipping a
// file for one reason and then saying it skipped it for another.
//
// The default arm covers absent and storage-only alike, which is what appspec/06
// asks for: its three keys are "already backed up / broken link / does not
// exist", and both of those states are "does not exist" AT HOME -- the only side
// this step looks at. It also covers real-file-present, which step 1 admits and
// so cannot normally reach here; the one way it can is a home path that exists
// as something other than a regular file or directory (a socket, a device, a
// FIFO, or a live symlink to one), which linkableSource excludes and which is
// "does not exist" as far as this command's step 1 is concerned.
func (r *linkRun) doingNothing(homePath, mackupPath string) {
	switch syncfs.StateOf(homePath, mackupPath) {
	case syncfs.StateAlreadyLinked:
		r.trace(doingNothingLinked, homePath)
	case syncfs.StateBrokenLink:
		r.trace(doingNothingBroken, homePath)
	default:
		r.trace(doingNothingAbsent, homePath)
	}
}

// linkableSource answers the first half of appspec/06 step 1's condition -- "the
// home path exists as a regular file or directory" -- with a two-valued answer,
// and the two-valued answer is a DECISION rather than the conflation it looks
// like.
//
// sourcePresent returns (bool, error) precisely so that its caller in sync.go
// cannot read a stat failure as an absence; four review rounds on the copy
// strategy went into establishing that a stat error is not an answer, and the
// same shape is now in sourcePresent, folderPresent and the storage-root gate.
// This function discards that error on purpose, which is the opposite call, so
// here is why the licence that justified those does not reach this command.
//
// The copy-side fix rests on appspec/01 section 5's unconditional sentence: "A
// partial backup/restore can never exit 0." A backup that silently skipped an
// unreadable source and exited 0 therefore contradicted the specification
// itself, and fixing it removed a contradiction rather than changing behaviour.
// Nothing of that kind is available here. appspec/00 promise 9 is titled "A
// partial copy is never reported as success (COPY MODE)" and then withholds the
// guarantee in as many words: "This honesty is asymmetric -- the link strategy
// does not uphold it as cleanly." appspec/07's error table has no row for an
// uninspectable home path under any link command. And appspec/01 section 5's
// permission to improve link failures is bounded by "without changing any
// successful-run behavior": the reference completes this run and exits 0, so
// that IS a successful run there, and failing it would change exactly what the
// permission excludes.
//
// So the reference's answer stands, and appspec/06 step 1's own wording is
// consistent with it: "Act only if the home path exists as a regular file or
// directory ... Otherwise ... nothing happens." A condition the program could
// not establish has not been satisfied, and the otherwise-arm is where it lands
// -- with the "does not exist" trace under --verbose, which is the one thing
// that keeps the skip observable.
//
// internal/syncfs.StateOf already made the same call for the same command, and
// its comment gives the safety argument this one does not repeat: reading an
// unstattable home path as a real file present "would send `link install` on to
// copy and delete it". Absent is the safe direction of the two.
//
// TestAHomePathThatCannotBeInspectedIsSkippedRatherThanFailingTheRun pins this,
// so a later reader who reaches for the copy-side fix has to argue with a case
// rather than with a comment.
func linkableSource(homePath string) bool {
	present, err := sourcePresent(homePath)
	return err == nil && present
}

// linkFailure is appspec/07's "Failure inside a link operation" row: stderr,
// non-zero exit, the run stopped mid-way, unguarded regime.
//
// fault.Unguarded is the regime because that is the column appspec/07 puts this
// row in, and the "mackup: " prefix that regime carries is what distinguishes it
// on stderr from the guarded "Error: " rows. appspec/07 permits a
// reimplementation to "collapse the unguarded cases into clean single-line
// exits" provided the stream, the exit and the no-further-effect post-condition
// hold, and a Go program has no traceback to print, so it takes that permission
// -- while keeping the regime observable, which internal/fault's own doc
// explains at length.
//
// The message names the path, which appspec/02 requires of this regime ("write a
// diagnostic naming the offending value to stderr"), and the operation, because
// the three operations of the per-file sequence fail for different reasons and a
// user reading "unable to remove ~/.vimrc" after the copy already succeeded is
// being told which half of the non-atomic window they are in.
func linkFailure(err error, format string, args ...any) error {
	return fault.Unguardedf("unable to %s: %s", fmt.Sprintf(format, args...), err)
}
