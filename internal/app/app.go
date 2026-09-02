// Package app is the program's startup pipeline: the fixed sequence of
// appspec/01-architecture.md section 4 (parse argv -> resolve config ->
// assemble the application database -> environment gate -> dispatch) and the
// exit codes of appspec/02-invocation.md.
package app

import (
	"errors"

	"github.com/promptctl/macklebox/internal/cli"
	"github.com/promptctl/macklebox/internal/ui"
	"github.com/promptctl/macklebox/internal/version"
)

// Exit codes, per the exit-code table of appspec/02-invocation.md.
const (
	// ExitOK means the requested action completed.
	ExitOK = 0
	// ExitFailure is the fatal, cleanly-handled error code.
	ExitFailure = 1
)

// ForceConflictMessage is a literal contract token (appspec/07 "Error behavior
// summary"): supplying both force flags writes exactly this line to stderr.
const ForceConflictMessage = "Options --force and --force-no are mutually exclusive."

// Main runs one invocation and returns the process exit code. argv excludes the
// program name.
func Main(argv []string, streams *ui.IO) int {
	// Step 1: parse argv.
	inv, err := cli.Parse(argv)
	if err != nil {
		var usageErr *cli.UsageError
		if errors.As(err, &usageErr) {
			// appspec/07 "Output streams": argument-parser usage and warning
			// text on a usage error goes to stderr.
			streams.Errf("mackup: %s\n", usageErr.Warning)
			streams.Errln(cli.Usage)
			return ExitFailure
		}
		streams.Errf("mackup: %s\n", err)
		return ExitFailure
	}

	// Still step 1: --help and --version short-circuit to stdout with exit 0,
	// before any config read. They are the only paths that both succeed and
	// skip the universal config-load gate.
	switch {
	case inv.Opts.Help:
		streams.Outln(cli.Usage)
		return ExitOK
	case inv.Opts.Version:
		streams.Outln(version.Banner())
		return ExitOK
	}

	// Still step 1: the mutually exclusive force flags are rejected here,
	// before config is loaded and before any action is performed.
	if inv.Opts.Force && inv.Opts.ForceNo {
		streams.Errln(ForceConflictMessage)
		return ExitFailure
	}

	// A bare invocation names no subcommand. appspec/02 "Argument-parser
	// behavior" treats it as a usage display rather than an error, so it
	// succeeds and never reaches the config gate.
	if inv.Cmd == cli.CmdNone {
		streams.Outln(cli.Usage)
		return ExitOK
	}

	return runPipeline(inv, streams)
}

// runPipeline carries a subcommand through the remaining startup stages. Each
// stage aborts the run for every subcommand alike -- including list and show,
// which otherwise touch no storage (appspec/01 section 1).
func runPipeline(inv cli.Invocation, streams *ui.IO) int {
	// Step 2: resolve the user config, which eagerly resolves the storage
	// location. Step 3: assemble the application database. Step 4/5: the
	// environment-gate lattice (root guard, storage root, Mackup folder).
	if err := loadConfig(inv); err != nil {
		streams.Errf("Error: %s\n", err)
		return ExitFailure
	}
	if err := assembleApplicationDatabase(inv); err != nil {
		streams.Errf("Error: %s\n", err)
		return ExitFailure
	}
	if err := environmentGate(inv); err != nil {
		streams.Errf("Error: %s\n", err)
		return ExitFailure
	}

	// Step 6: dispatch to the requested subcommand.
	return dispatch(inv, streams)
}
