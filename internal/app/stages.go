package app

import (
	"os"

	"github.com/promptctl/macklebox/internal/appdb"
	"github.com/promptctl/macklebox/internal/cli"
	"github.com/promptctl/macklebox/internal/config"
	"github.com/promptctl/macklebox/internal/fault"
	"github.com/promptctl/macklebox/internal/homepath"
)

// The startup stages between argv parsing and dispatch. They exist as named
// seams so the dispatch order of appspec/02-invocation.md ("Command dispatch
// order and the universal config-load gate") is expressed by the pipeline
// rather than assumed, and so each resolver drops into its own place instead of
// restructuring Main. All three are filled in now; the order they are called
// in from runPipeline IS the dispatch order appspec/02 specifies.

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

// resolveHome is the home directory the per-file paths of appspec/06 "Shared
// vocabulary" are built on: "home path: $HOME/f".
//
// It is a startup fact of the same kind as the storage location, not a
// property of the config, which is why it is resolved here rather than added
// to config.Config -- appspec/03 gives that type five properties and $HOME is
// not one of them; it is an input to resolving them.
//
// It re-asks a question config.Load has already answered, and cannot fail once
// that stage has passed. That duplication is deliberate and internal/homepath
// argues it for its other caller in the same words: "a package whose
// correctness depends on the order its caller happens to run stages in has no
// contract of its own to test." Here the same reasoning applies to the
// pipeline -- the sync commands need an absolute home, and taking it on faith
// from a stage above would make a reordering of the pipeline produce paths
// under a relative root rather than a diagnostic.
func resolveHome() (string, error) {
	return homepath.Require(os.Getenv("HOME"))
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
// The two checks are in the order appspec/01 section 4 writes them, and the
// order is observable: a superuser run against a machine with no storage
// folder reports the guard rather than the folder. Refusing root first is also
// the only order that keeps the guard's promise -- appspec/01 says effective
// UID 0 "aborts any command BEFORE IT DOES WORK", and a stat of a path the
// superuser can reach is work the guard was meant to prevent.
func environmentGate(inv cli.Invocation, cfg *config.Config) error {
	if err := refuseSuperuser(inv.Opts.Root); err != nil {
		return err
	}
	return requireStorageRoot(cfg.Root())
}

// effectiveUID reports the process's effective user id.
//
// A seam for the same reason internal/version indirects debug.ReadBuildInfo:
// appspec/07 marks the root-refusal path UNVERIFIED because "the harness ran
// only as a non-root user", and a guard whose refusing arm no test can take is
// a guard a passing suite says nothing about. The conformance suite observes
// the permitted arm -- it runs as an ordinary user, where every command must
// work with and without --root -- and internal/app's own tests drive both arms
// through here.
var effectiveUID = os.Geteuid

// refuseSuperuser implements appspec/07 "The superuser (root) guard".
//
// "If the process's effective user id is 0 (running as superuser) and --root /
// -r was not given, the program writes a fatal error to stderr warning that
// running as superuser can be dangerous and to run `mackup --help` for
// guidance, and exits 1." Running as an ordinary user always passes, and
// --root then has no observable effect.
//
// A guarded BLOCK rather than a Guardedf sentence: appspec/07's error table
// gives this row the guarded column but does not give it an "Error: " sentence
// the way it gives one to the storage-root row below, and the information
// content it does specify is two things -- the danger and where to look --
// which is the shape its two multi-line guarded neighbours take.
func refuseSuperuser(rootAllowed bool) error {
	if effectiveUID() != 0 || rootAllowed {
		return nil
	}
	return fault.GuardedBlock(
		"Running Mackup as a superuser is dangerous: it would sync the root account's\n" +
			"configuration, and every file it wrote would be owned by root.\n" +
			"Pass --root if you really mean to, and run `mackup --help` for guidance.")
}

// requireStorageRoot implements the other half of appspec/01 section 4's level
// 1: the storage-root directory has to exist.
//
// This is the stage appspec/04 defers its file_system existence check TO. That
// engine "returns the path without any existence check" by design (clause 2),
// and this is the one place the uniform "Unable to find the storage folder:
// <path>" of appspec/07's table is raised -- for every engine alike, so a
// Dropbox root whose host database decoded to a folder that has since been
// removed fails here exactly as a user-supplied path does.
//
// A directory, not merely something that exists: appspec/01 calls the check
// "storage-root DIRECTORY exists", and the sync engine of appspec/06 creates
// the Mackup folder inside it. A regular file at that path cannot hold one,
// and reporting it as found would move the failure to the first copy.
func requireStorageRoot(root string) error {
	// Stat and not Lstat: a symlink to the real storage directory is how a
	// user points ~/Dropbox at a volume, and the directory it resolves to is
	// the one the folder is created in.
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return fault.Guardedf("Unable to find the storage folder: %s", root)
	}
	return nil
}
