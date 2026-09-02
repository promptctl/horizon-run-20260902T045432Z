package app

import "github.com/promptctl/macklebox/internal/cli"

// The startup stages between argv parsing and dispatch are stubs until the
// resolver tickets land. They exist now so the dispatch order of
// appspec/02-invocation.md ("Command dispatch order and the universal
// config-load gate") is expressed by the pipeline rather than assumed, and so
// each resolver drops into a named seam instead of restructuring Main.

// loadConfig resolves the user config file and, with it, the storage location
// (appspec/03, appspec/04).
//
// TODO(macklebox-resolvers-5iw.2): implement; a failure here must abort every
// subcommand, including list and show.
func loadConfig(cli.Invocation) error { return nil }

// assembleApplicationDatabase builds the key -> (display name, file set) table
// from the layered definition directories (appspec/05).
//
// TODO(macklebox-resolvers-5iw.3): implement.
func assembleApplicationDatabase(cli.Invocation) error { return nil }

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
// TODO(macklebox-resolvers-5iw.4): implement the root guard and storage-root
// existence check.
func environmentGate(cli.Invocation) error { return nil }
