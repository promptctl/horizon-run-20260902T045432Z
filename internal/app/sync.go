package app

import (
	"fmt"
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
// the two-level sorted fan-out (section 1), single-application scoping
// (section 3, in scope.go), the Mackup-folder gate (section 4, in folder.go),
// the one confirmation policy (section 3, in confirm.go), dry-run and verbose
// (section 3), and the partial-failure contract (section 5). None of it is
// backup-specific: appspec/01 says the five sync commands are "five leaves on
// one tree, not five independent programs", and the link tickets plug into
// these same functions.

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
	verb string
	// driftPhrasing fills the "<f> differs between <...>:" header: "home and
	// Mackup" or "Mackup and home". The order matches the direction, so the
	// header reads source-first exactly as the diff below it is rendered.
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

// A syncRun is one invocation of the copy operation: the direction, the two
// run-mode booleans, the confirmation policy, and the failures collected so
// far.
//
// The two booleans and the policy are fields rather than reads of a global,
// because appspec/01 section 3 says the confirmation decision "is passed as
// data, not read from an ambient global" and gives dry-run and verbose as one
// uniform rule each. Threading them means a test can build a run in any of the
// eight combinations without touching the process.
type syncRun struct {
	dir     direction
	streams *ui.IO
	confirm confirmer
	apps    *appdb.Database

	// home and folder are the two roots of appspec/06 "Shared vocabulary":
	// $HOME and <storage-root>/<directory>.
	home   string
	folder string

	dryRun  bool
	verbose bool

	// failed is appspec/06's partial-failure contract as data. "Failures flow
	// up as data, not as control flow -- the loop never aborts and the process
	// never exits from inside the per-file copy."
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
	keys, known := selectApplications(p.inv, p.cfg, p.apps)
	if !known {
		// The same token, level and stream `show` uses for the same condition
		// -- appspec/07's table gives it one row, not one per command.
		p.streams.Say(ui.Fatal, UnsupportedApplicationPrefix+p.inv.Application)
		return ExitFailure
	}

	run := &syncRun{
		dir:     dir,
		streams: p.streams,
		confirm: confirmer{policy: policyOf(p.inv.Opts), streams: p.streams},
		apps:    p.apps,
		home:    p.home,
		folder:  p.cfg.MackupFolder(),
		dryRun:  p.inv.Opts.DryRun,
		verbose: p.inv.Opts.Verbose,
	}

	if err := run.gate(); err != nil {
		return reportFatal(p.streams, err)
	}
	if err := run.fanOut(keys); err != nil {
		// The one way out of the loop that is not a per-file failure: end of
		// input at a prompt (appspec/07, unguarded). Everything a copy can do
		// wrong is already in run.failed by now.
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

// fanOut is the two-level loop of appspec/01 section 1: "applications in sorted
// key order; files within each application in sorted path order; each file
// handled independently."
//
// Neither level sorts. appdb.Keys and appdb.Files both return sorted values and
// config.Scope preserves the order it is given, so the guarantee is made once
// by the database rather than re-made here where it could disagree.
//
// It returns an error only for a failure that ends the run. A per-file copy
// failure is recorded and the loop continues -- that is appspec/06's
// partial-failure contract, and writing it as a returned error would be the
// exact "failure as control flow" the contract forbids.
func (r *syncRun) fanOut(keys []string) error {
	for _, key := range keys {
		files, known := r.apps.Files(key)
		if !known {
			// An allowlisted key with no definition. config.Scope already
			// promises its result is a subset of the keys it was given, so
			// this is unreachable today; skipping rather than failing is the
			// answer that keeps a configuration naming an application this
			// build does not ship from aborting a whole backup.
			continue
		}
		r.header(key)
		for _, relative := range files {
			if err := r.file(relative); err != nil {
				return err
			}
		}
	}
	return nil
}

// header prints the per-application verbose header of appspec/07: "per-app
// verbose header uses blue (34) rules around a bold app name".
//
// One line, and one Say, built from two levels with ui.Colorize -- which is
// what that function is exported for, and the shape its doc names. Written as
// three Says it would be three messages; written as one uncoloured string it
// would lose the bold name appspec/07 asks for.
//
// Verbose only. appspec/01 section 3 says verbose "swaps short progress lines
// for long ones (full absolute source/destination paths, a per-app header
// rule)" -- the header is one of the things verbose adds, and printing it
// otherwise would put an application's name on stdout for an application that
// turns out to have nothing to do.
func (r *syncRun) header(key string) {
	if !r.verbose {
		return
	}
	name, known := r.apps.Name(key)
	if !known {
		name = key
	}
	r.streams.Say(ui.AppRule, "--- "+ui.Colorize(ui.AppName, name)+" ---")
}

// file is appspec/06 "The shared per-file procedure", step for step.
//
// Its four numbered steps are the four blocks below, in that order, and the
// numbering is load-bearing: the link-skip is asked BEFORE the destination is
// looked at, so a file that was link-installed is skipped rather than compared
// against the very copy its home path points to -- a comparison that would
// report identical and then, on any implementation that treated identity
// differently, copy a file over itself.
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
	if !existsAsFileOrDirectory(src) {
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

	// Step 4 is taken first because it is the simpler branch: nothing at the
	// destination means no comparison, no diff and no prompt.
	//
	// os.Lstat, so a symlink AT the destination counts as present and is
	// prompted about as a "link". Following it would make restore silently
	// overwrite whatever a home symlink pointed at, which is a file the user
	// never named.
	existing, err := os.Lstat(dst)
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
		// appspec/01 section 3: dry-run "prints the progress line it would
		// emit for each acted-on file, then performs no copy, move, delete, or
		// symlink". The prompt is inside the mutation it guards, so it is not
		// shown either -- a dry run that asked "are you sure you want to
		// replace it?" would be asking about a replacement it is not going to
		// perform, and under --force it would perform nothing while reporting
		// a decision.
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

// progress prints appspec/06's progress line, in whichever of its two forms
// the run mode calls for.
//
// Short: "<verb> <f> ...". Verbose: the four-line form with absolute paths,
// which appspec/06 writes as "<verb>\n  <src>\n  to\n  <dst> ...".
//
// One Say per line, for the reason reportFatal and list split theirs:
// appspec/07 promises "every colored string is terminated with a reset", and a
// multi-line message coloured as one string opens a colour on its middle lines
// that it never closes.
//
// ui.Progress in both forms. Verbose changes WHICH progress line is printed,
// not what class of message it is -- appspec/01 section 3 says verbose "swaps
// short progress lines for long ones", and appspec/07 lists the per-file
// progress lines under stdout without qualification. Only the skip traces are
// ui.Verbose.
func (r *syncRun) progress(relative, src, dst string) {
	if !r.verbose {
		r.streams.Sayf(ui.Progress, "%s %s ...", r.dir.verb, relative)
		return
	}
	r.streams.Say(ui.Progress, r.dir.verb)
	r.streams.Say(ui.Progress, "  "+src)
	r.streams.Say(ui.Progress, "  to")
	r.streams.Sayf(ui.Progress, "  %s ...", dst)
}

// trace prints one of the two verbose-only skip traces of appspec/06.
//
// Verbose is observationally pure (appspec/01 section 3: "it changes only what
// is printed, never what is done or the exit code"), which is why this is the
// whole of what the flag does at a skip: the caller has already decided to
// skip, and this only says so.
func (r *syncRun) trace(format string, args ...any) {
	if !r.verbose {
		return
	}
	r.streams.Sayf(ui.Verbose, format, args...)
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

// report writes the end-of-run summary of appspec/06's partial-failure
// contract and returns the exit code.
//
// "So a backup or restore that could not copy everything NEVER exits 0 -- the
// non-zero exit and the stderr summary distinguish a partial run from a
// complete one (00, promise 9)."
//
// The count comes from the same slice the lines come from, so the two cannot
// disagree -- the lesson `list`'s count trailer records, one command over.
func (r *syncRun) report() int {
	if len(r.failed) == 0 {
		return ExitOK
	}
	r.streams.Sayf(ui.CopyFailure, "%s incomplete: %d file(s) could not be copied:",
		r.dir.summaryNoun, len(r.failed))
	for _, path := range r.failed {
		r.streams.Say(ui.CopyFailure, "  "+path)
	}
	return ExitFailure
}

// existsAsFileOrDirectory is appspec/06's step-1 test, worded as that step
// words it.
//
// It follows symlinks, and the per-file procedure explains why. A shared helper
// rather than an inline stat so that the step-1 question and syncfs.Copy's
// classification are visibly the same question.
func existsAsFileOrDirectory(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular() || info.IsDir()
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
