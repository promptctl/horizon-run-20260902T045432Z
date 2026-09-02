package app

import (
	"github.com/promptctl/macklebox/internal/appdb"
	"github.com/promptctl/macklebox/internal/cli"
	"github.com/promptctl/macklebox/internal/config"
)

// The startup stages between argv parsing and dispatch. They exist as named
// seams so the dispatch order of appspec/02-invocation.md ("Command dispatch
// order and the universal config-load gate") is expressed by the pipeline
// rather than assumed, and so each resolver drops into its own place instead of
// restructuring Main. The last of them is still a stub; the ticket that fills
// it is named on it.

// loadConfig resolves the user config file and, with it, the storage location
// (appspec/03, appspec/04).
//
// It is a seam of two lines because everything it does belongs to the packages
// it calls: internal/config owns discovery, parsing and validation, and
// internal/storage owns the four engines. What this function contributes is
// the pipeline position -- appspec/02 requires the config to load before
// dispatch for EVERY subcommand, so a failure here aborts list and show
// exactly as it aborts the sync commands.
//
// The environment is read here rather than inside config.Load so that the one
// place the process's own environment is consulted is on the path from Main,
// where every other ambient input is read too.
func loadConfig(inv cli.Invocation) (*config.Config, error) {
	return config.Load(
		config.Override{Path: inv.Opts.ConfigFile, Set: inv.Opts.ConfigFileSet},
		config.EnvironmentFromOS(),
	)
}

// assembleApplicationDatabase builds the key -> (display name, file set) table
// from the layered definition directories (appspec/05).
//
// A seam of one line for the reason loadConfig is: internal/appdb owns the
// definition format, the three-directory precedence and the two rejections. What
// this function contributes is the pipeline position -- appspec/01 section 4
// puts database assembly at step 3, after the config and before every gate, so a
// definition holding an absolute path aborts list and show exactly as it aborts
// a sync command.
//
// It does not take the config. The two stages read the same environment and
// neither reads the other's result: appspec/03's application lists are applied
// to the assembled keys by the commands that act on "all applications", not by
// assembly, and appspec/05's discovery does not consult the config at all.
// Threading a *config.Config here would suggest otherwise.
func assembleApplicationDatabase() (*appdb.Database, error) {
	return appdb.Assemble(appdb.EnvironmentFromOS())
}

// environmentGate runs level 1 of the lattice in appspec/01 section 4 -- the
// root guard and storage-root existence -- which every command passes
// identically.
//
// Levels 2 and 3, the per-command Mackup-folder gate, deliberately do NOT
// belong here: appspec/02 requires a named <application> to be validated
// before the environment check for its command, "so that an unknown
// application fails cleanly before any folder is created or any prompt is
// shown". Creating the folder from this seam would make `backup frobnicate`
// prompt and create before rejecting the key.
//
// It takes the config because the storage-root existence check is a check on
// the root the config resolved -- and that is where appspec/04's deliberately
// missing file_system existence check is finally enforced.
//
// TODO(macklebox-resolvers-5iw.4): implement the root guard and storage-root
// existence check.
func environmentGate(cli.Invocation, *config.Config) error { return nil }
