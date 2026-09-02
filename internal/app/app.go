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
	code := runArgv(argv, streams)

	// A run whose output never reached the user is not a completed action, so
	// it cannot report one. Only the closed-pipe case on stdout/stderr is
	// handled for us (Go raises SIGPIPE there); a redirect to a full disk is
	// not, and would otherwise exit 0 with truncated output.
	if err := streams.WriteError(); err != nil {
		streams.Sayf(ui.Fatal, "Error: unable to write output: %s", err)
		if code == ExitOK {
			return ExitFailure
		}
	}
	return code
}

func runArgv(argv []string, streams *ui.IO) int {
	// Step 1: parse argv.
	inv, err := cli.Parse(argv)
	if err != nil {
		var usageErr *cli.UsageError
		if errors.As(err, &usageErr) {
			// appspec/07 "Output streams": argument-parser usage and warning
			// text on a usage error goes to stderr. The warning is the
			// diagnostic and is coloured -- appspec/02's exit-code table calls
			// a fatal a "single colored diagnostic line" -- while the usage
			// block after it is the parser's reference text, which appspec/07
			// gives no level and appspec/02 declares non-contract wording. So
			// it is routed but not coloured; see runArgv's help arm.
			streams.Sayf(ui.Fatal, "mackup: %s", usageErr.Warning)
			streams.Errln(cli.Usage)
			return ExitFailure
		}
		streams.Sayf(ui.Fatal, "mackup: %s", err)
		return ExitFailure
	}

	// Still step 1: --help and --version short-circuit to stdout with exit 0,
	// before any config read.
	//
	// The version banner is Progress -- appspec/07's "normal progress / info"
	// -- so it is yellow on stdout, and the done-claim of this ticket names it
	// as one of the two paths that must still show SGR when output is piped.
	// The usage block is not: appspec/07's colour scheme assigns no level to
	// the argument parser's own text, and appspec/02 states its wording is
	// human-facing and not a machine-read contract. Routing it is the contract
	// here; colouring it would be inventing a level the spec does not list.
	switch {
	case inv.Opts.Help:
		streams.Outln(cli.Usage)
		return ExitOK
	case inv.Opts.Version:
		streams.Say(ui.Progress, version.Banner())
		return ExitOK
	}

	// Still step 1: the mutually exclusive force flags are rejected here,
	// before config is loaded and before any action is performed.
	if inv.Opts.Force && inv.Opts.ForceNo {
		// Coloured, and the token survives it: Colorize wraps a whole message
		// and never splits one, so the literal line appspec/07 makes contract
		// stays contiguous for a script grepping stderr. ui.StripColor is the
		// same property read from the other end, and the conformance suite
		// asserts the token through it.
		streams.Say(ui.Fatal, ForceConflictMessage)
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
		streams.Sayf(ui.Fatal, "Error: %s", err)
		return ExitFailure
	}
	if err := assembleApplicationDatabase(inv); err != nil {
		streams.Sayf(ui.Fatal, "Error: %s", err)
		return ExitFailure
	}
	if err := environmentGate(inv); err != nil {
		streams.Sayf(ui.Fatal, "Error: %s", err)
		return ExitFailure
	}

	// Step 6: dispatch to the requested subcommand.
	return dispatch(inv, streams)
}
