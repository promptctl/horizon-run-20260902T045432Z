package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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

// linkInstallProgress is appspec/06 step 2's progress line for this command,
// which is the one place a command needs two different words: "Linking <f> ..." in the
// short form, and "Backing up\n  <home>\n  to\n  <mackup> ..." in the verbose
// four-line form. The verbose word is backup's, and that is what appspec/06
// writes -- it is not a copy-paste slip to be tidied into "Linking".
var linkInstallProgress = copyProgress("Linking", "Backing up")

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
	r.progress(linkInstallProgress, relative, homePath, mackupPath)
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

// -- `link`: symlink Mackup files into home, moving nothing out of home -----
//
// The SECOND of Strategy B's two entry doors, and appspec/00's two-entry-doors
// section is emphatic that it is a different contract from the first rather
// than a variation of it: link install "is for the machine that HAS the real
// files and is adopting the tool", where this "is for a second machine that
// already sees the shared folder (populated by the first machine) and wants to
// adopt those settings: it only CREATES THE SYMLINKS pointing at files already
// in the shared folder; it moves nothing out of home". appspec/00 puts
// collapsing the two at the head of its list of ways to build "a different
// product", so the procedure below is written out rather than folded into
// install with a flag.
//
// The consequence to hold on to while reading it is the direction: the source
// is the MACKUP path and the destination is the HOME path, which is the reverse
// of link install and the reason the guards look transposed against it. The
// three-line difference from link install is the whole command -- no copy into
// storage, no delete of a home file that is not being replaced, and a storage
// side this command only ever reads (and clamps; see the note on step 4).

// linkProgress is appspec/06 step 2's progress line for `link`.
//
// The SHAPE differs from every other command's and not only the word.
// appspec/06 writes it as "Restoring\n  linking <home>\n  to      <mackup> ...",
// which is three lines where backup, restore and link install print four, with
// the destination on the "to" line rather than below it and the run of spaces
// aligning the two paths under each other. That is why progressForm carries a
// template rather than a verb: this is the command that made a verb
// insufficient.
//
// The short word is "Restoring", which is neither of the words the copy
// commands use ("Recovering" for restore) and is a third spelling of a
// neighbouring idea. appspec/06 writes it, so it stands.
var linkProgress = progressForm{
	short: "Restoring",
	long:  "Restoring\n  linking %s\n  to      %s ...",
}

// linkReplaceQuestion is appspec/07's "Replace an existing home file on link"
// prompt, verbatim.
//
// It names the HOME path, where link install's names the mackup path, because
// the thing about to be destroyed is the home file. Like link install's it
// carries no --force hint; appspec/07 gives that parenthetical to backup's
// prompt alone.
const linkReplaceQuestion = "You already have a %s at %s. " +
	"Do you want to replace it with your backup?"

// doingNothingPlatform is the verbose skip trace for appspec/06 step 1's third
// condition, "the file is allowed to be synced on this platform".
//
// It names the home path, like the other traces, and says which rule skipped
// the file: appspec/00 "Platform assumptions" lists this as one of exactly
// three places behavior differs between macOS and Linux, and a user comparing
// two machines' output is owed the reason rather than a bare "Doing nothing".
const doingNothingPlatform = "Doing nothing\n  %s\n  is under ~/Library, which is not linked on Linux"

// runLink is the whole of `link` for one application: gate, fan out, exit.
//
// The gate is level 3 of appspec/01 section 4's lattice -- REQUIRE -- which
// appspec/06 "Environment gate per command" assigns to "restore, link, link
// uninstall" together: the Mackup folder must ALREADY exist and is never
// created. That is the shape of the command rather than a detail of it. This is
// the join-an-existing-sync door, so a machine with no Mackup folder has
// nothing to join, and creating an empty one would turn that into a run which
// silently links nothing and exits 0.
//
// Everything else is runLinkInstall's, for runLinkInstall's reasons: the named
// application is validated before the gate (appspec/06 names `link` in that
// rule too), there is no failure aggregation, and the one way out of the loop
// is appspec/07's "Failure inside a link operation" row.
//
// TODO(macklebox-link-sync-83q.1): with no application named, appspec/06
// "Whole-Mackup mode" gives this command a ceremony -- the `mackup` application
// linked FIRST, then the config and application database reloaded mid-run, then
// the rest of the set from the reloaded config. Until that ticket lands an
// unscoped run does the plain fan-out over the configured scope, which links
// the same files in the same order and differs only in that a `.mackup.cfg`
// this run itself links does not take effect for the applications after it.
func runLink(p pipeline) int {
	keys, known := resolveScope(p)
	if !known {
		return ExitFailure
	}

	run := &linkRun{executor: newExecutor(p)}
	if err := requireMackupFolder(run.folder); err != nil {
		return reportFatal(p.streams, err)
	}
	if err := run.fanOut(keys, run.join); err != nil {
		return reportFatal(p.streams, err)
	}
	return ExitOK
}

// join is appspec/06 "`link` -- symlink Mackup files into home (no move out of
// home)", step for step. Its four numbered steps are the four blocks below.
//
// The name is the ticket's and appspec/00's -- "the join-an-existing-sync
// path" -- rather than "link", because a method called link on a type called
// linkRun would read as the whole strategy and this is one of its three
// commands.
func (r *linkRun) join(relative string) error {
	homePath := filepath.Join(r.home, relative)
	mackupPath := filepath.Join(r.folder, relative)

	// Step 1, whose three conditions are the three arms below, in the order
	// appspec/06 lists them: "act only if ALL of: the mackup path exists as a
	// regular file or directory; the home path is not already a symlink to the
	// mackup path; and the file is allowed to be synced on this platform. ...
	// If any condition fails, verbose prints a 'Doing nothing ...' trace and
	// nothing happens."
	//
	// Storage first, because this command reads FROM storage and a machine
	// that has not yet received a file has nothing this command can do with it
	// -- which on a second machine mid-sync is the ordinary case, not a corner.
	// The order is otherwise free: a file the predicate calls already-linked
	// necessarily has a mackup copy, so those two arms cannot both apply.
	//
	// linkableSource and not a second stat written here, so that this
	// condition and link install's are visibly the same sentence of appspec/06
	// asked of the other end. Its argument for reading a stat failure as an
	// absence is recorded there and covers this caller too.
	if !linkableSource(mackupPath) {
		r.trace(doingNothingAbsent, mackupPath)
		return nil
	}

	// syncfs.AlreadyLinked, the one predicate of appspec/01 section 2. This is
	// the third of its four call sites, and it is what makes this command
	// idempotent -- appspec/06's "link install / link: an already-linked file
	// is skipped", which is appspec/00 promise 3's fixed point for both doors.
	if syncfs.AlreadyLinked(homePath, mackupPath) {
		r.trace(doingNothingLinked, homePath)
		return nil
	}

	// The platform rule, which no other command has: appspec/00 "Platform
	// assumptions" scopes it to "the plain `link` command" alone, so it is
	// asked here and not in the executor or in syncfs.
	if !linkableOnPlatform(runtime.GOOS, relative) {
		r.trace(doingNothingPlatform, homePath)
		return nil
	}

	// Step 2. "Print progress (`Restoring <f> ...`, or verbose `Restoring\n
	// linking <home>\n  to      <mackup> ...`). If dry-run, stop here for this
	// file."
	r.progress(linkProgress, relative, homePath, mackupPath)
	if r.dryRun {
		return nil
	}

	// Steps 3 and 4, which are one question -- "is something already at the
	// home path?" -- with the prompt on one side of it. The mirror of link
	// install's, asked of the other end: there the question is about the copy
	// in storage, here about the file in home, because that is what each is
	// about to destroy.
	//
	// os.Lstat, so a symlink at the home path counts as present and is
	// prompted about as a "link". That is not a nicety here: appspec/01 section
	// 2's Foreign state is a home path pointing somewhere else, and following
	// the link would take the delete on the yes arm to the file it points at
	// rather than to the link -- destroying a file the user never named while
	// leaving the link in place for os.Symlink to fail on.
	//
	// A home path that could not be INSPECTED is neither step 3 nor step 4.
	// Falling through to step 4 would claim nothing is there on the strength of
	// a stat that established nothing, and would skip the only prompt guarding
	// a file the program cannot see. It is a weaker hazard than link install's
	// same guard -- there the fall-through copies and then deletes the home
	// file, where here os.Symlink refuses an occupied path with EEXIST, so no
	// constructible fixture loses data through it -- and it is still not this
	// program's to assert. The failure regime is the link one, so the run stops
	// rather than recording and carrying on.
	existing, err := os.Lstat(homePath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return linkFailure(err, "inspect %s", homePath)
	}
	if err == nil {
		// Step 3. "If a file/dir/link already exists at the home path: prompt
		// ... On yes: delete the home path, then create a symlink at the home
		// path pointing to the mackup copy. On no: do nothing for this file."
		replace, err := r.confirm.Ask(fmt.Sprintf(
			linkReplaceQuestion, typeNoun(existing), homePath))
		if err != nil {
			return err
		}
		if !replace {
			// A declined prompt is a deliberate answer and not a failure, for
			// the reason install's arm gives: appspec/00 promise 4 makes
			// declining the default of the safety gate, and --force-no
			// pre-answers every prompt with no.
			return nil
		}
		if err := syncfs.Delete(homePath); err != nil {
			return linkFailure(err, "replace %s", homePath)
		}
	}

	// Step 4, and the tail of step 3: "create the symlink directly."
	//
	// ONE mutation, where link install's move is three, and that is appspec/06's
	// "Net effect" for this command: "the Mackup copies are not modified". The
	// storage side is read and never written -- no copy runs, so nothing here
	// can carry home content into the shared folder, which is the specific way
	// a `link` that had been folded into link install would corrupt a second
	// machine's join by overwriting the first machine's file with whatever
	// happened to be at the home path.
	//
	// "Not modified" is about CONTENT. syncfs.Link clamps its target
	// recursively before creating the link, which appspec/06 states as a
	// post-condition of the primitive ("Before linking, target's permissions
	// are set recursively"), so a storage file that arrived group-readable
	// through a sync client is narrowed to 0600 by this run. That is the
	// specified behaviour and the one place in the program where the clamp
	// meets a path no copy of the same run created.
	if err := syncfs.Link(mackupPath, homePath); err != nil {
		return linkFailure(err, "link %s to %s", homePath, mackupPath)
	}
	return nil
}

// linkableOnPlatform answers appspec/06 step 1's third condition for `link`:
// "the file is allowed to be synced on this platform. The platform rule: on
// Linux, a file whose home path is under `~/Library/` is not synced (skipped);
// on macOS there is no such restriction."
//
// A pure function of (goos, relative path) rather than a runtime.GOOS test
// inline, for the reason syncfs.attributeCleanups is one: appspec/00 "Platform
// assumptions" lists exactly three places behavior differs between macOS and
// Linux, and the platform TABLE is the part worth pinning. Inline, the rule can
// only ever be observed on the machine the suite happens to run on, and the
// half that does not apply there is untested -- which for a rule whose whole
// content is "these two platforms differ" is the half that matters.
//
// The test is a prefix on the HOME-RELATIVE path, which is what "under
// ~/Library/" means once the home directory is factored out, and it is the
// separator that carries the meaning: a definition naming "Library" alone is
// the directory itself rather than something under it, and appspec/06 words the
// rule as "under", so that path is linked. "Librarian.cfg" is likewise not
// under ~/Library/ and a prefix test without the separator would have skipped
// it.
//
// The forward slash is the separator appspec/05 gives definition paths, and the
// rule only fires on Linux, where it is the operating system's separator too --
// so there is no platform on which this has to ask filepath what a separator is.
func linkableOnPlatform(goos, relative string) bool {
	return goos != "linux" || !strings.HasPrefix(relative, "Library/")
}

// linkableSource answers the source half of appspec/06 step 1's condition for a
// link command -- "exists as a regular file or directory" -- with a two-valued
// answer, and the two-valued answer is a DECISION rather than the conflation it
// looks like.
//
// Which path is the SOURCE differs by command and the two callers pass their
// own: link install asks it of the home path ("act only if the home path
// exists as a regular file or directory"), `link` of the mackup path ("act only
// if ... the mackup path exists as a regular file or directory"). It is one
// function because it is one sentence of appspec/06, asked of whichever end
// that command reads from; the argument below is about the two-valued answer
// and holds for both.
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
// TestAHomePathThatCannotBeInspectedIsSkippedRatherThanFailingTheRun pins this
// for link install and TestAMackupPathThatCannotBeInspectedIsSkippedByLink for
// `link`, so a later reader who reaches for the copy-side fix has to argue with
// two cases rather than with a comment.
//
// For `link` the skip is additionally the harmless direction in a way it is not
// for link install: link install leaves a file unmanaged, where `link` leaves a
// home file that was never going to be read from anyway. The alternative there
// -- proceeding on a mackup path the program could not inspect -- would delete
// the user's real home file and point the home path at something unknown, which
// is the failure mode of `link` and the reason its own destination guard below
// exists.
func linkableSource(sourcePath string) bool {
	present, err := sourcePresent(sourcePath)
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
