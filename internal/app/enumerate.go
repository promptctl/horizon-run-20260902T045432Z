package app

import (
	"github.com/promptctl/macklebox/internal/appdb"
	"github.com/promptctl/macklebox/internal/ui"
	"github.com/promptctl/macklebox/internal/version"
)

// The two enumeration commands of appspec/05-application-database.md
// "Enumeration": `list`, which prints every application key and a count
// trailer, and `show <key>`, which prints one application's display name and
// file paths.
//
// They are the program's audit surface (appspec/00 promise 5: "list and show
// let the user see the whole catalog and any one application's exact file set
// before running anything"), and they are the only commands that read the
// application database without acting on a file. Everything they print comes
// from the three lookups appspec/05 exposes -- Keys, Name, Files -- and
// nothing here re-derives a key from a filename or a path from a definition.

// UnsupportedApplicationPrefix is a literal contract token (appspec/07 "Error
// behavior summary" names it among "the literal tokens that ARE contract
// (matched by scripts/tests)"): a command given an application key the
// database does not hold writes this prefix followed by the key.
//
// Exported for the same reason ForceConflictMessage is: it is a token a script
// greps for, so the program states it once and the tests assert the program's
// own constant rather than a copy that can drift from it.
const UnsupportedApplicationPrefix = "Unsupported application: "

// listHeader opens `list` output. appspec/05 gives the whole block literally;
// this is its first line.
const listHeader = "Supported applications:"

// showNamePrefix and showFilesHeader are the two labels of `show` output,
// spelled as appspec/05 spells them.
const (
	showNamePrefix  = "Name: "
	showFilesHeader = "Configuration files:"
)

// entryPrefix opens each key line of `list` and each path line of `show`.
// appspec/05 writes both as " - <value>", with the leading space.
const entryPrefix = " - "

// list prints every application key and the supported-count trailer
// (appspec/05 "Enumeration").
//
//	Supported applications:
//	 - <key>
//	 ...
//
//	<N> applications supported in Mackup v<version>
//
// The keys are what the database hands over, in the order it hands them over:
// appdb.Keys already returns them sorted ascending, which is the order
// appspec/05 asks for, and sorting them again here would be a second
// implementation of the same rule for the next reader to have to compare.
//
// It does NOT apply the config's application lists, and that is contract
// rather than an omission. appspec/03 says of the allowlist, in as many words,
// that "this section does not affect `list` output", and appspec/05 defines
// what list prints as "the set of ALL keys assembled by the discovery rules" --
// which is also what makes list an audit surface: a user narrowing their sync
// scope still needs to see the catalog the narrowing is drawn from. The two
// lists select what a SYNC command acts on, and config.Scope is theirs.
//
// The count is the number of keys printed, not a number computed some other
// way. appspec/05's observed effect -- dropping a definition into ~/.mackup
// "makes key myapp appear in list, increments the supported-count trailer by
// one" -- is one claim about two lines of output, and deriving them separately
// is how they come to disagree.
func list(streams *ui.IO, apps *appdb.Database) int {
	keys := apps.Keys()

	// Every line at ui.Progress, which appspec/07 assigns "normal progress /
	// info" and routes to stdout -- the stream it names for "list output"
	// outright. One Say per line, including the blank separator: appspec/07
	// promises "every colored string is terminated with a reset", and a
	// multi-line message written as one coloured string leaves its middle
	// lines opening a colour they never close. That is the same reason
	// reportFatal splits a diagnostic, and the reason the blank line here is
	// an empty message at a level rather than a bare newline down a named
	// stream: ui.Outln exists for the argument parser's usage block, which is
	// the one text appspec/07 gives no level.
	streams.Say(ui.Progress, listHeader)
	for _, key := range keys {
		streams.Say(ui.Progress, entryPrefix+key)
	}
	streams.Say(ui.Progress, "")
	streams.Sayf(ui.Progress, "%d applications supported in Mackup v%s", len(keys), version.String())
	return ExitOK
}

// show prints one application's display name and file set (appspec/05
// "Enumeration").
//
//	Name: <Display Name>
//	Configuration files:
//	 - <path>
//	 ...
//
// The paths come from appdb.Files, which returns them sorted ascending and
// home-relative -- appspec/05's own two guarantees about the file set, made by
// the database rather than re-made here.
//
// A known application with an EMPTY file set still prints both labels and no
// path lines. appspec/05 makes that state valid and observable in as many
// words: "A definition with neither contributes an application that has an
// empty file set (it still appears in list and show)." Suppressing the
// "Configuration files:" label there would make an application with no
// authored paths indistinguishable from one this program failed to read.
//
// An unknown key is the failure appspec/07's table gives the literal token
// `Unsupported application: <name>`, on stderr, exit 1. It is written directly
// rather than through internal/fault: fault carries the two config-failure
// regimes of appspec/01 section 6, and this is neither -- appspec/07's table
// leaves its regime column empty, because the run reached its command and the
// command refused the argument it was given.
func show(streams *ui.IO, apps *appdb.Database, key string) int {
	name, known := apps.Name(key)
	if !known {
		// The token stays contiguous through colouring: ui.Colorize wraps a
		// whole message and never splits one, so a script grepping stderr for
		// the prefix finds it in a line appspec/02 requires to be coloured.
		streams.Say(ui.Fatal, UnsupportedApplicationPrefix+key)
		return ExitFailure
	}

	// Files is asked for the key Name has already answered for, so the second
	// result cannot be false here -- both read the same map. Discarded rather
	// than re-checked: a second "unknown application" branch would be an arm
	// no input can take, and appspec/05 has exactly one such failure.
	files, _ := apps.Files(key)

	streams.Say(ui.Progress, showNamePrefix+name)
	streams.Say(ui.Progress, showFilesHeader)
	for _, path := range files {
		streams.Say(ui.Progress, entryPrefix+path)
	}
	return ExitOK
}
