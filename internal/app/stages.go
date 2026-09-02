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

// environmentGate runs the three-level lattice of appspec/01 section 4: the
// root guard and storage-root existence for every command, plus the
// per-command Mackup-folder gate.
//
// TODO(macklebox-resolvers-5iw.4): implement the usable-environment level.
func environmentGate(cli.Invocation) error { return nil }
