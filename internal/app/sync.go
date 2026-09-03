package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/promptctl/macklebox/internal/appdb"
	"github.com/promptctl/macklebox/internal/cli"
	"github.com/promptctl/macklebox/internal/config"
	"github.com/promptctl/macklebox/internal/drift"
	"github.com/promptctl/macklebox/internal/syncfs"
	"github.com/promptctl/macklebox/internal/ui"
)

// backup and restore: ONE copy operation, parameterized by direction.
//
// appspec/01-architecture.md section 1 states the contract this file exists to
// keep: "Backup and restore are not two mirrored procedures -- they are one
// copy procedure run in opposite directions, with a small data record
// supplying every difference ... any divergence between backup and restore
// other than {direction, user-facing wording, the one link-skip} is a defect."
//
// So there is one procedure below and one `direction` record. The way to add a
// difference is to add a FIELD, which makes the difference reviewable as data
// next to every other difference; the way to introduce a defect is to write
// `if d.fromHome` inside the procedure. There is exactly one such test in the
// per-file path -- the link-skip -- and appspec/01 names it as the one genuine
// behavioural asymmetry.
//
// This is also where the system-wide machinery of appspec/01 is first used:
// the two-level sorted fan-out and the per-application verbose header (section
// 1, in executor.go), single-application scoping (section 3, in scope.go), the
// Mackup-folder gate (section 4, in folder.go), the one confirmation policy
// (section 3, in confirm.go), dry-run and verbose (section 3), and the
// partial-failure contract (section 5). None of it is backup-specific:
// appspec/01 says the five sync commands are "five leaves on one tree, not
// five independent programs", so everything on that list but the last item is
// in executor.go, which link.go runs too. The partial-failure contract is the
// exception and stays here, because appspec/01 section 5 gives it to the copy
// commands ALONE -- the link commands fail hard mid-run instead.

// A folderGate names which level of appspec/01 section 4's lattice a direction
// runs. It is part of the direction record because the fifth gate is the ONLY
// per-command variation in the startup pipeline, and appspec/06 assigns backup
// the ensure gate and restore the require gate -- a difference of direction
// (you create the folder you are writing into; you demand the folder you are
// reading from), which is why it is data here and not a branch below.
type folderGate int

const (
	gateEnsure folderGate = iota
	gateRequire
)

// A direction is appspec/06's direction record, given there as a table. Every
// field below is one column of it.
type direction struct {
	// source and destination are supplied by fromHome rather than by two path
	// fields, because they are one fact: appspec/06's table has backup read
	// the home path and write the mackup path, and restore the reverse.
	fromHome bool

	// verb is the progress verb: "Backing up" or "Recovering".
	//
	// One word and not a progressVerbs pair, because appspec/06 gives the copy
	// operation the same word in both forms of its progress line. link install
	// is the command that needs two, and it says so where it names its own.
	verb string
	// driftPhrasing fills the "<f> differs between <...>:" header: "home and
	// Mackup" or "Mackup and home", source first. The diff below it runs the
	// other way round, and deliberately: its REMOVED side is the destination,
	// the content about to be replaced.
	driftPhrasing string
	// location is the destination-location noun the replace prompt names:
	// "the Mackup folder" or "your home folder".
	location string
	// mentionsForce is appspec/06's "mentions --force?" column: backup's
	// prompt appends the hint, restore's does not.
	mentionsForce bool
	// linkSkip is the one genuine behavioural asymmetry. Backup skips a home
	// file that is already a symlink into the Mackup folder; restore has no
	// such case, because the mackup copy is always the real file.
	linkSkip bool
	// gate is the environment gate of appspec/01 section 4 level 2 or 3.
	gate folderGate
	// summaryNoun opens the end-of-run partial-failure summary: "Backup
	// incomplete: ..." or "Restore incomplete: ...".
	summaryNoun string
}

// The two rows of appspec/06's direction table, written once.
var (
	backupDirection = direction{
		fromHome:      true,
		verb:          "Backing up",
		driftPhrasing: "home and Mackup",
		location:      "the Mackup folder",
		mentionsForce: true,
		linkSkip:      true,
		gate:          gateEnsure,
		summaryNoun:   "Backup",
	}

	restoreDirection = direction{
		fromHome:      false,
		verb:          "Recovering",
		driftPhrasing: "Mackup and home",
		location:      "your home folder",
		mentionsForce: false,
		linkSkip:      false,
		gate:          gateRequire,
		summaryNoun:   "Restore",
	}
)

// endpoints orients one file's two absolute paths (appspec/06 "Shared
// vocabulary") into the source and destination of this direction.
func (d direction) endpoints(homePath, mackupPath string) (src, dst string) {
	if d.fromHome {
		return homePath, mackupPath
	}
	return mackupPath, homePath
}

// forceHint is the parenthetical appspec/07 gives backup's replace prompt and
// withholds from restore's: "(use --force to skip this prompt)".
func (d direction) forceHint() string {
	if d.mentionsForce {
		return " (use --force to skip this prompt)"
	}
	return ""
}

// A syncRun is one invocation of the copy operation: the shared executor, the
// direction, and the failures collected so far.
//
// Everything a run needs that is not specific to copying -- the streams, the
// confirmation policy, the application database, the two roots, dry-run and
// verbose -- is the embedded executor's, because those are the same facts read
// the same way by all five sync commands (appspec/01 section 1). What is left
// here is exactly what appspec/06 and appspec/01 section 5 make specific to
// backup and restore.
type syncRun struct {
	*executor

	dir direction

	// failed is appspec/06's partial-failure contract as data. "Failures flow
	// up as data, not as control flow -- the loop never aborts and the process
	// never exits from inside the per-file copy."
	//
	// It is here and not on the executor because appspec/01 section 5 gives
	// this contract to backup and restore ALONE: "link commands have no
	// failure-aggregation path". A slice on the shared executor would offer
	// the three link commands somewhere to put failures they are specified not
	// to collect.
	failed []string
}

// runSync is the whole of backup and restore: gate, fan out, aggregate, exit.
//
// The order is appspec/06's "Environment gate per command" and appspec/01
// section 3, and it is observable: the application name is validated first, so
// `backup frobnicate` reports the unsupported key WITHOUT creating a folder or
// showing a prompt, which is one clause of this ticket's done-claim and the
// reason folder.go is not called from the startup pipeline.
func runSync(p pipeline, dir direction) int {
	keys, known := resolveScope(p)
	if !known {
		return ExitFailure
	}

	run := &syncRun{executor: newExecutor(p), dir: dir}

	if err := run.gate(); err != nil {
		return reportFatal(p.streams, err)
	}
	if err := run.fanOut(keys, run.file); err != nil {
		// The one way out of the loop that is not a per-file failure: end of
		// input at a prompt (appspec/07, unguarded). Everything a copy can do
		// wrong is already in run.failed by now -- which is why the summary is
		// emitted here rather than skipped: appspec/06 makes it the end-of-run
		// report of a run that could not copy everything, and a run that ends
		// this way could not. Without it the user gets the per-file error line
		// and never the list of which files to go back to.
		run.summarize()
		return reportFatal(p.streams, err)
	}
	return run.report()
}

// gate runs level 2 or level 3 of appspec/01 section 4's lattice, whichever
// this direction names.
func (r *syncRun) gate() error {
	if r.dir.gate == gateEnsure {
		return ensureMackupFolder(r.confirm, r.folder)
	}
	return requireMackupFolder(r.folder)
}

// file is appspec/06 "The shared per-file procedure", step for step.
//
// Its four numbered steps are the four blocks below, and the numbering is
// load-bearing: the link-skip is asked BEFORE the destination is looked at, so
// a file that was link-installed is skipped rather than compared against the
// very copy its home path points to -- a comparison that would report
// identical and then, on any implementation that treated identity differently,
// copy a file over itself.
func (r *syncRun) file(relative string) error {
	homePath := filepath.Join(r.home, relative)
	mackupPath := filepath.Join(r.folder, relative)
	src, dst := r.dir.endpoints(homePath, mackupPath)

	// Step 1. "If the source path does not exist as a regular file or
	// directory, skip it silently."
	//
	// os.Stat, so a symlink is judged by what it points at -- which is what
	// makes a BROKEN home symlink absent here (nothing to copy) while a live
	// one carries on to step 2. It is also the classification syncfs.Copy
	// makes, so a file this step admits is one that primitive can act on.
	//
	// Silently, with no verbose trace. appspec/06 gives backup and restore
	// exactly two verbose traces -- the link-skip below and "already in sync"
	// -- and its "nothing printed unless a verbose trace applies" is satisfied
	// by naming which ones exist. A third message here would be one the
	// specification does not have, printed once per unconfigured file, which
	// on a 614-application catalog is thousands of lines of the program saying
	// nothing happened.
	//
	// A stat that failed for any reason OTHER than absence is the third
	// answer, and it is a failure rather than a skip. See sourcePresent.
	present, err := sourcePresent(src)
	if err != nil {
		r.fail(src, dst, err)
		return nil
	}
	if !present {
		return nil
	}

	// Step 2, backup only. "If the source is already a symlink to its mackup
	// path (already backed up via link install), skip it."
	//
	// syncfs.AlreadyLinked, not a symlink test written here. appspec/01
	// section 2: "a reimplementer who codes this check four times risks four
	// subtly different answers; there must be one definition." This is one of
	// the four call sites; link install, link and link uninstall are the
	// others.
	//
	// The predicate is asked of (home, mackup) and not of (src, dst), because
	// it is a question about the home path being a link INTO storage. They
	// coincide for backup, which is the only direction that asks -- but
	// spelling it in the direction's terms would make it read as though
	// restore could meaningfully ask it too, in reverse.
	if r.dir.linkSkip && syncfs.AlreadyLinked(homePath, mackupPath) {
		r.trace("Skipping %s, already linked to %s", homePath, mackupPath)
		return nil
	}

	// os.Lstat, so a symlink AT the destination counts as present and is
	// prompted about as a "link". Following it would make restore silently
	// overwrite whatever a home symlink pointed at, which is a file the user
	// never named.
	existing, err := os.Lstat(dst)

	// A destination that could not be INSPECTED is neither step 3 nor step 4.
	// Both of those ask whether a copy is there, and a stat that failed for
	// any reason other than ENOENT did not answer -- so the procedure may not
	// pick a branch, and least of all the unguarded one. Reading every error
	// as "nothing is there" sends an uninspectable destination straight to
	// syncfs.Copy, which does not require an absent destination (O_CREATE
	// without O_EXCL, MkdirAll for a tree), past the comparison, the diff and
	// the replace prompt that appspec/06 step 3 exists to put in front of
	// exactly that write. On a network or FUSE home an ESTALE or EIO would be
	// enough to overwrite a file the user was never asked about, and the run
	// would exit 0 with no record that a guard had been skipped.
	//
	// appspec/06's partial-failure contract already has the shape this needs:
	// an error line, the path recorded as data, the run carrying on, a
	// non-zero exit and the file named in the summary. No progress line, and
	// that is the point of putting the guard here rather than after one -- the
	// progress line announces a copy, and this file never got as far as
	// deciding to make one. Before the dry-run check for the same reason: a
	// dry run that cannot see the destination cannot say what a real run would
	// do, and "would copy" is precisely the claim it has no basis for.
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		r.fail(src, dst, err)
		return nil
	}

	// Step 4 is taken before step 3 because it is the simpler branch: nothing
	// at the destination means no comparison, no diff and no prompt.
	if err != nil {
		r.progress(relative, src, dst)
		if r.dryRun {
			return nil
		}
		r.copy(src, dst)
		return nil
	}

	// Step 3. A copy already exists at the destination, so compare.
	result := drift.Compare(src, dst)
	if result.Identical {
		// "This is the idempotency fixed point -- a second run with no
		// underlying change does nothing and prompts for nothing."
		r.trace("%s already in sync, skipping", relative)
		return nil
	}

	r.progress(relative, src, dst)
	if r.dryRun {
		// Above the header, the diff AND the prompt, because appspec/06 step
		// 3 puts all three behind one condition: "Then, IF NOT DRY-RUN: ...
		// print the header ... followed by the diff ..., then prompt". A dry
		// run owes a differing file the progress line and nothing further
		// (appspec/01 section 3). The prompt is the plainest of the three: it
		// would ask about a replacement that is not going to happen, and under
		// --force report a decision while performing nothing.
		return nil
	}

	// The drift header is THIS file's, not internal/drift's: appspec/06 gives
	// its wording from the direction record, which drift knows nothing about.
	// ui.Anomaly, which is STDOUT -- appspec/07 names this exact message under
	// "Do not generalize warnings -> stderr". It reads like a bug and is not.
	//
	// Only when there is detail. appspec/06: an empty detail means the paths
	// are "not content-comparable (the caller then shows the plain prompt with
	// no diff)", and a header over nothing would promise a diff that never
	// arrives.
	if len(result.Detail) > 0 {
		r.streams.Sayf(ui.Anomaly, "%s differs between %s:", relative, r.dir.driftPhrasing)
		result.Print(r.streams)
	}

	replace, err := r.confirm.Ask(fmt.Sprintf(
		"A %s named %s already exists in %s. Are you sure that you want to replace it?%s",
		typeNoun(existing), dst, r.dir.location, r.dir.forceHint()))
	if err != nil {
		return err
	}
	if !replace {
		// "On no, skip this file." Not a failure: appspec/06 puts a declined
		// prompt among the states a converging re-run handles, and counting it
		// as an uncopyable file would make a deliberate answer produce the
		// non-zero exit that appspec/00 promise 9 reserves for a run that
		// could not do what it was asked.
		return nil
	}
	if err := syncfs.Delete(dst); err != nil {
		// Reported as a copy failure, because from the run's side that is what
		// it is: the file could not be copied, and the reason is that its
		// destination could not be removed first. appspec/06 gives the
		// per-file channel one shape and one summary, and inventing a second
		// for the delete half of a replace would split one contract in two.
		r.fail(src, dst, err)
		return nil
	}
	r.copy(src, dst)
	return nil
}

// progress prints this direction's progress line through the executor.
//
// appspec/06 gives backup and restore the same word in both forms and the same
// four-line verbose shape link install prints, so the form handed over is
// copyProgress with one word twice. Built here rather than stored on the
// direction record, because the record is appspec/06's direction TABLE and that
// table has one verb column; a second, always-equal column would read as a
// difference between backup and restore that appspec/01 section 1 says must not
// exist.
func (r *syncRun) progress(relative, src, dst string) {
	r.executor.progress(copyProgress(r.dir.verb, r.dir.verb), relative, src, dst)
}

// copy performs the one mutation of the per-file procedure and records a
// failure rather than raising one.
//
// syncfs.Copy already clamps the destination to 0600/0700 recursively, so
// there is no chmod here. appspec/06 states the clamp once as a post-condition
// of the primitive, and a second one at this call site would be a second
// implementation of the same rule for a later reader to have to reconcile.
func (r *syncRun) copy(src, dst string) {
	if err := syncfs.Copy(src, dst); err != nil {
		r.fail(src, dst, err)
	}
}

// fail is appspec/06's per-file copy-failure line, and the record that feeds
// the end-of-run summary.
//
// ui.CopyFailure: stderr, red rather than the bright red of a fatal, because
// appspec/07 distinguishes the two by meaning -- "the first means the program
// stopped and the second means it carried on".
//
// The recorded path is the SOURCE. The line above it names both paths, so the
// summary needs only one to identify the file, and the source is the one the
// user recognizes: it is the file they asked to have copied, in the place they
// keep it. It is also absolute, which the relative path is not: two
// applications can name the same relative path, and a summary listing it twice
// with no way to tell the runs apart is worse than one that is merely long.
func (r *syncRun) fail(src, dst string, err error) {
	r.streams.Sayf(ui.CopyFailure, "Error: Unable to copy %s to %s: %s", src, dst, err)
	r.failed = append(r.failed, src)
}

// summarize writes the end-of-run summary of appspec/06's partial-failure
// contract.
//
// Separate from the exit code because the two have different audiences. Every
// way a run can END owes the summary -- including the fatal path, where the
// run stops at an unanswerable prompt with failures already collected -- while
// only the ordinary path chooses an exit code from it. Written as one function
// the fatal path had to call it and discard the result, which reads as an
// oversight whether or not it is one.
//
// The count comes from the same slice the lines come from, so the two cannot
// disagree -- the lesson `list`'s count trailer records, one command over.
func (r *syncRun) summarize() {
	if len(r.failed) == 0 {
		return
	}
	r.streams.Sayf(ui.CopyFailure, "%s incomplete: %d file(s) could not be copied:",
		r.dir.summaryNoun, len(r.failed))
	for _, path := range r.failed {
		r.streams.Say(ui.CopyFailure, "  "+path)
	}
}

// report ends an ordinary run: the summary, then the exit code appspec/06 ties
// to it.
//
// "So a backup or restore that could not copy everything NEVER exits 0 -- the
// non-zero exit and the stderr summary distinguish a partial run from a
// complete one (00, promise 9)."
func (r *syncRun) report() int {
	r.summarize()
	if len(r.failed) == 0 {
		return ExitOK
	}
	return ExitFailure
}

// sourcePresent is appspec/06's step-1 test, worded as that step words it, plus
// the third answer the step's wording assumes away.
//
// It follows symlinks, and the per-file procedure explains why. A shared helper
// rather than an inline stat so that the step-1 question and syncfs.Copy's
// classification are visibly the same question.
//
// The (bool, error) shape is folderPresent's, for folderPresent's reason: a
// stat that fails for anything but ENOENT establishes NEITHER answer, and
// returning false for it makes step 1 assert something the program never found
// out. Here the cost is the worst in the file. A source read as absent is
// skipped SILENTLY -- no line, nothing recorded in r.failed -- so a home file
// under a directory whose search bit another machine's account cleared, or a
// path on a network mount answering ESTALE, is not copied and the run still
// exits 0. appspec/01 section 5 is unconditional about that outcome: "A partial
// backup/restore can never exit 0", and appspec/00 promise 9 gives the reason a
// user feels -- "a clean exit means everything the user asked for is in place".
// Silence plus exit 0 is strictly worse than the destination case above, which
// at least printed a line.
//
// This diverges from the reference, which reaches step 1 through Python's
// os.path.isfile/isdir and so answers false for every stat error. It is the
// same reference defect the folder and storage-root gates carry, fixed the same
// way and for a stronger reason: there the divergence only moved a diagnostic,
// and here the reference contradicts a contract its own specification states.
// A run whose sources are all inspectable is byte-identical either way.
func sourcePresent(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular() || info.IsDir(), nil
}

// typeNoun is the <type> of appspec/07's prompts: "one of file, folder, or
// link, describing the existing path".
//
// It is given an Lstat result, so a symlink is reported as a link rather than
// as whatever it points at. That is the whole reason the noun exists: "You
// already have a link at ~/.vimrc" tells the user something a claim about a
// file does not, and the thing being replaced is the link.
func typeNoun(info os.FileInfo) string {
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return "link"
	case info.IsDir():
		return "folder"
	default:
		return "file"
	}
}

// A pipeline is everything the startup stages of appspec/01 section 4 resolved,
// handed to the command that will use it.
//
// It exists so that dispatch's arms take one value rather than a growing
// argument list: the five sync commands all need the same five facts, and a
// signature they each repeat is one a later ticket edits five times.
type pipeline struct {
	inv     cli.Invocation
	streams *ui.IO
	apps    *appdb.Database
	cfg     *config.Config

	// home is $HOME, the root of the home paths of appspec/06 "Shared
	// vocabulary". Resolved once by the pipeline rather than per file.
	home string
}
