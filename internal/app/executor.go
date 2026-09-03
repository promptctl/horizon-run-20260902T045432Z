package app

import (
	"fmt"
	"strings"

	"github.com/promptctl/macklebox/internal/appdb"
	"github.com/promptctl/macklebox/internal/ui"
)

// An executor is the one uniform per-file executor of
// appspec/01-architecture.md section 1: the two-level sorted fan-out, the
// deferred per-application verbose header, the verbose trace, the progress line
// in its two forms, and the run-mode facts every sync command reads the same
// way.
//
// appspec/01 draws the program as "four resolvers feeding one uniform per-file
// executor" and says the five sync commands "are five leaves on one tree, not
// five independent programs". This is that executor.
//
// What it deliberately does NOT hold is the per-file procedure. appspec/06
// gives backup/restore one procedure and link install, link and link uninstall
// three more, and appspec/00's "two entry doors" section warns outright against
// collapsing two of them into one. So the varying part is a function the caller
// supplies, and the invariant part -- which applications, in what order, which
// files inside them, in what order, and which of them gets a header -- is
// written once here. A command that wrote its own loop would be re-deciding an
// ordering appspec/01 states as a whole-program guarantee.
//
// The division of labour against internal/syncfs is worth stating because both
// are "shared": syncfs is the shared VOCABULARY (copy, delete, link, the clamp,
// the predicate) and knows nothing of configuration, streams or the user; this
// is the shared PROCEDURE SHELL, which knows all three and moves no bytes.
type executor struct {
	streams *ui.IO
	confirm confirmer
	apps    *appdb.Database

	// home and folder are the two roots of appspec/06 "Shared vocabulary":
	// $HOME and <storage-root>/<directory>.
	home   string
	folder string

	// The two run-mode booleans of appspec/01 section 3, threaded as data for
	// the reason the confirmation policy is: a test can build a run in any
	// combination without touching the process.
	dryRun  bool
	verbose bool

	// pendingHeader is the application whose verbose header is owed but not
	// yet printed, or "" when none is. See header and flushHeader.
	pendingHeader string
}

// newExecutor assembles the executor from what the startup pipeline resolved.
//
// One constructor, so the five commands cannot disagree about which run-mode
// flag means what or about where the two roots come from. The gate is NOT run
// here: appspec/01 section 4 makes it the one per-command variation in the
// startup pipeline, and appspec/06 assigns different levels to different
// commands, so it stays with the command that knows which level it runs.
func newExecutor(p pipeline) *executor {
	return &executor{
		streams: p.streams,
		confirm: confirmer{policy: policyOf(p.inv.Opts), streams: p.streams},
		apps:    p.apps,
		home:    p.home,
		folder:  p.cfg.MackupFolder(),
		dryRun:  p.inv.Opts.DryRun,
		verbose: p.inv.Opts.Verbose,
	}
}

// fanOut is the two-level loop of appspec/01 section 1: "applications in sorted
// key order; files within each application in sorted path order; each file
// handled independently."
//
// Neither level sorts. appdb.Keys and appdb.Files both return sorted values and
// config.Scope preserves the order it is given, so the guarantee is made once
// by the database rather than re-made here where it could disagree.
//
// The per-file procedure is the parameter, and its error is the ONE way out of
// the loop. What that means differs by command and is the commands' to decide:
// appspec/01 section 5 gives backup and restore a partial-failure contract
// under which a copy failure is recorded as data and never returned here, and
// gives the link commands no aggregation path at all, so a failure inside one
// of those is exactly what ends the run. Both shapes fit one loop because the
// loop takes no view: it stops when the procedure says to stop.
func (e *executor) fanOut(keys []string, file func(relative string) error) error {
	for _, key := range keys {
		files, known := e.apps.Files(key)
		if !known {
			// An allowlisted key with no definition. config.Scope already
			// promises its result is a subset of the keys it was given, so
			// this is unreachable today; skipping rather than failing is the
			// answer that keeps a configuration naming an application this
			// build does not ship from aborting a whole run.
			continue
		}
		e.header(key)
		for _, relative := range files {
			if err := file(relative); err != nil {
				return err
			}
		}
	}
	return nil
}

// header records which application's verbose header is owed. It does not print
// it; flushHeader does, on that application's first line of output.
//
// Deferred rather than printed, because printing it here says an application's
// name for an application that turns out to have nothing to do -- which is the
// hazard the old comment on this function named and then did not avoid.
// appspec/06's step 1 skips a file whose source does not exist SILENTLY, so on
// an unscoped run the overwhelming majority of the catalog produces no output
// at all: measured on a home with one real file, an eager header gave 623
// stdout lines of which 614 were headers. The nine lines that were the actual
// run are what verbose exists to show.
//
// Nothing in the specification asks for the eager shape. appspec/01 section 3
// says verbose "swaps short progress lines for long ones (full absolute
// source/destination paths, a per-app header rule)" and appspec/07 gives the
// header its colours; neither promises one per catalog key.
func (e *executor) header(key string) {
	e.pendingHeader = key
}

// flushHeader prints the per-application verbose header of appspec/07: "per-app
// verbose header uses blue (34) rules around a bold app name".
//
// One line, and one Say, built from two levels with ui.Colorize -- which is
// what that function is exported for, and the shape its doc names. Written as
// three Says it would be three messages; written as one uncoloured string it
// would lose the bold name appspec/07 asks for.
//
// Called from trace and progress and from nowhere else: they are the only two
// that can produce an application's first STDOUT line, which is what this
// header groups. The drift header and the replace prompts are reachable only
// after progress has run for the same file. backup's per-file failure line is
// NOT -- the uninspectable-destination guard reaches it with no progress line
// before it -- and is left unflushed deliberately: it writes to stderr, and
// names both paths in full, so it needs no header to say which application it
// belongs to. Flushing there would put a header on stdout with nothing under
// it, which is the one thing
// TestTheVerboseHeaderIsPrintedOnlyForAnApplicationThatPrintsSomething
// forbids outright.
func (e *executor) flushHeader() {
	if !e.verbose || e.pendingHeader == "" {
		return
	}
	key := e.pendingHeader
	e.pendingHeader = ""
	name, known := e.apps.Name(key)
	if !known {
		name = key
	}
	e.streams.Say(ui.AppRule, "--- "+ui.Colorize(ui.AppName, name)+" ---")
}

// trace prints one of appspec/06's verbose-only skip traces.
//
// Verbose is observationally pure (appspec/01 section 3: "it changes only what
// is printed, never what is done or the exit code"), which is why this is the
// whole of what the flag does at a skip: the caller has already decided to
// skip, and this only says so.
//
// The trace may be several lines -- link install's "Doing nothing ..." traces
// are, and appspec/06 writes them that way -- so each line is its own coloured
// message, for the reason appspec/07 gives: "every colored string is
// terminated with a reset", and a multi-line message coloured as one string
// opens a colour on its middle lines that it never closes.
func (e *executor) trace(format string, args ...any) {
	if !e.verbose {
		return
	}
	e.flushHeader()
	e.sayLines(ui.Verbose, format, args...)
}

// A progressForm is one command's progress line of appspec/06, in the two
// forms the run mode chooses between.
//
// Two fields and not one word, because appspec/06 varies BOTH halves across
// the five commands and varies them independently. link install uses two
// different words -- "Linking <f> ..." short against "Backing up\n  <home>\n
// to\n  <mackup> ..." verbose -- where backup and restore use one word twice.
// And `link` changes the verbose SHAPE as well as the word: appspec/06 writes
// it as "Restoring\n  linking <home>\n  to      <mackup> ...", three lines
// where the copy commands print four, with the destination on the "to" line
// rather than below it.
//
// So the verbose half is a template rather than a verb. Modelling it as a verb
// and special-casing the one command whose shape differs would put that
// command's layout inside the shared executor; modelling it as data keeps the
// executor able to say "print this command's progress line" and nothing more.
// copyProgress builds the shape three of the five share, so that generality
// costs those three nothing.
type progressForm struct {
	// short is the word that opens the short form, "<short> <f> ...".
	short string
	// long is the whole verbose form, as a format template given the absolute
	// source and destination paths in that order.
	long string
}

// copyProgress is the four-line verbose shape of appspec/06 -- "<verb>\n
// <src>\n  to\n  <dst> ..." -- which backup, restore and link install all
// print and `link` does not.
//
// A constructor rather than three copies of the template, for the reason
// appspec/01 section 1 gives about backup and restore generally: three
// spellings of one layout are three chances for it to drift, and the drift
// would be invisible until someone compared two commands' verbose output side
// by side.
func copyProgress(short, long string) progressForm {
	return progressForm{short: short, long: long + "\n  %s\n  to\n  %s ..."}
}

// progress prints appspec/06's progress line, in whichever of its two forms the
// run mode calls for.
//
// Short: "<short verb> <f> ...", which every command spells the same way and
// which is therefore built here. Verbose: the command's own template, filled
// with the absolute source and destination.
//
// ui.Progress in both forms. Verbose changes WHICH progress line is printed,
// not what class of message it is -- appspec/01 section 3 says verbose "swaps
// short progress lines for long ones", and appspec/07 lists the per-file
// progress lines under stdout without qualification. Only the skip traces are
// ui.Verbose.
func (e *executor) progress(form progressForm, relative, src, dst string) {
	e.flushHeader()
	if !e.verbose {
		e.streams.Sayf(ui.Progress, "%s %s ...", form.short, relative)
		return
	}
	e.sayLines(ui.Progress, form.long, src, dst)
}

// sayLines writes a formatted message one line per Say.
//
// The reason is appspec/07's "every colored string is terminated with a reset":
// a multi-line message coloured as a single string opens a colour on its middle
// lines that it never closes, and a terminal carries that into whatever prints
// next. reportFatal splits its diagnostic for the same reason, and this is that
// rule for the two multi-line shapes the executor itself prints.
func (e *executor) sayLines(level ui.Level, format string, args ...any) {
	for _, line := range strings.Split(fmt.Sprintf(format, args...), "\n") {
		e.streams.Say(level, line)
	}
}
