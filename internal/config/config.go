// Package config resolves the user's configuration file, and with it the
// storage location, as appspec/03-configuration.md specifies.
//
// appspec/03 describes the result rather than the process: "a small immutable
// value with exactly five observable properties -- the storage root path, the
// sub-directory name, the derived full Mackup-folder path (root joined to
// sub-directory), the ignore set, and the allow set", resolved EAGERLY, so
// that "construction either yields a fully-resolved value or terminates the
// process; there is no lazy/partial config." Config has those five properties
// and no setters, and Load is the only way to obtain one. That is why a bad
// storage engine breaks even commands that never sync: by the time any
// subcommand runs, the storage root has already been resolved or the program
// has already stopped.
//
// The failure behavior is as much of the contract as the success. Every
// diagnostic appspec/07's error table gives a config or storage row is built
// here or in internal/storage, carrying the regime appspec/01 section 6 puts
// it in; see internal/fault for how the two regimes stay distinguishable.
package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/promptctl/macklebox/internal/fault"
	"github.com/promptctl/macklebox/internal/homepath"
	"github.com/promptctl/macklebox/internal/ini"
	"github.com/promptctl/macklebox/internal/storage"
)

// Section names, spelled as appspec/03 spells them. Presence is by exact name,
// so these are compared literally and never folded.
const (
	storageSection = "storage"
	syncSection    = "applications_to_sync"
	ignoreSection  = "applications_to_ignore"
)

// The two pre-migration section names of appspec/03 "Legacy config rejection".
// Finding either aborts the program before anything else in the file is read.
// They are capitalized and spaced, which only the exact-name matching of
// appspec/03 keeps distinguishable from an ordinary unknown section.
const (
	legacyAllowedSection = "Allowed Applications"
	legacyIgnoredSection = "Ignored Applications"
)

// Keys of the [storage] section, and the defaults appspec/03 gives the two
// that have one.
const (
	engineKey    = "engine"
	pathKey      = "path"
	directoryKey = "directory"

	// defaultDirectory is the sub-folder name used when [storage] directory is
	// absent.
	defaultDirectory = "Mackup"
)

// defaultConfigName is the config file in the home directory: the first
// discovery candidate, and the fallback used when no candidate exists.
const defaultConfigName = ".mackup.cfg"

// xdgConfigName is the third candidate's path under the XDG config base. Note
// the missing leading dot -- appspec/03 calls it out, because the file is
// ".mackup.cfg" in the home directory and "mackup.cfg" here.
var xdgConfigName = filepath.Join("mackup", "mackup.cfg")

// An Environment is the part of the process environment appspec/03's
// "Environment variables that affect configuration" table names.
//
// Passed in as a value rather than read from os.Getenv inside Load, so that a
// test can state the environment a case is about instead of mutating the
// process's own. EnvironmentFromOS is the one place the real one is read.
type Environment struct {
	// Home is $HOME. appspec/03: it "must be set for the program to
	// function".
	Home string
	// MackupConfig is $MACKUP_CONFIG, the second discovery candidate.
	MackupConfig string
	// XDGConfigHome is $XDG_CONFIG_HOME, the base of the third.
	XDGConfigHome string
}

// EnvironmentFromOS reads the environment the process was started with.
func EnvironmentFromOS() Environment {
	return Environment{
		Home:          os.Getenv("HOME"),
		MackupConfig:  os.Getenv("MACKUP_CONFIG"),
		XDGConfigHome: os.Getenv("XDG_CONFIG_HOME"),
	}
}

// An Override is the -c / --config-file option as the argument parser recorded
// it.
//
// Set distinguishes "not given" from "given as the empty string", which the
// parser preserves and this package needs: an unset option means discovery
// runs, while an empty one is a path the user named and so must be resolved
// and reported like any other.
//
// It mirrors cli.Options rather than sharing it, so that resolving a config
// does not depend on the shape of the command line.
type Override struct {
	Path string
	Set  bool
}

// A Config is the resolved configuration: the five properties appspec/03 says
// it has, and nothing else.
//
// The fields are unexported and there are no setters. appspec/01 section 7
// notes that the config is re-RESOLVED mid-run in one place -- the no-argument
// link path reloads it after linking the user's own Mackup config -- which
// means building a second Config from Load, not mutating this one.
type Config struct {
	root      string
	directory string
	ignore    map[string]bool
	allow     map[string]bool
}

// Root is the storage root: the directory the sub-directory is created inside,
// as the storage engine resolved it (appspec/04).
func (c *Config) Root() string { return c.root }

// Directory is the name of the sub-folder inside the storage root that holds
// the synced files.
func (c *Config) Directory() string { return c.directory }

// MackupFolder is the full path synced files live under -- appspec/03's
// derived property, "root joined to sub-directory".
func (c *Config) MackupFolder() string { return filepath.Join(c.root, c.directory) }

// Ignored is the denylist of [applications_to_ignore], as sorted keys.
func (c *Config) Ignored() []string { return sortedKeys(c.ignore) }

// Allowed is the allowlist of [applications_to_sync], as sorted keys. Empty
// when the section is absent or holds nothing, which appspec/03 makes mean
// "all apps" rather than "no apps"; Scope is where that is decided.
func (c *Config) Allowed() []string { return sortedKeys(c.allow) }

// Scope returns the applications a command acting on "all applications" acts
// on, given every key in the application database.
//
// appspec/03 "Combined precedence of the two lists" states it as two steps:
// start from the allowlist if it is present and non-empty, otherwise from all
// keys, then remove everything in the denylist. So an application in BOTH
// lists is ignored -- the denylist wins -- and that is the case a
// reimplementation is most likely to get backwards.
//
// The order of all is preserved, which for the callers appspec/01 describes is
// sorted key order. An allowlisted key with no definition in the database is
// not invented: the result is a subset of all.
//
// Naming an application on the command line overrides both lists (appspec/02),
// and that override is the caller's -- it replaces this call rather than
// changing its result.
func (c *Config) Scope(all []string) []string {
	scope := make([]string, 0, len(all))
	for _, key := range all {
		if len(c.allow) > 0 && !c.allow[key] {
			continue
		}
		if c.ignore[key] {
			continue
		}
		scope = append(scope, key)
	}
	return scope
}

// Load discovers, reads and validates the user config file, and resolves the
// storage location eagerly.
//
// It returns either a fully-resolved Config or an error carrying the
// diagnostic and the regime appspec/07's table gives that condition. There is
// no third outcome: a partially-resolved config is exactly what appspec/03
// says cannot exist.
func Load(override Override, env Environment) (*Config, error) {
	home, err := homeDirectory(env)
	if err != nil {
		return nil, err
	}

	path, err := configPath(override, env, home)
	if err != nil {
		return nil, err
	}
	if !homepath.Inside(path, home) {
		// appspec/03 "Home-directory containment": checked at construction
		// and applied to a discovered path as much as to an explicitly named
		// one, which is why this sits outside configPath's two branches
		// rather than inside the explicit one.
		return nil, fault.Guardedf("The config file '%s' is not in your home directory. Aborting.", path)
	}

	content, err := readConfig(path)
	if err != nil {
		return nil, err
	}
	parsed := ini.Parse(content, ini.LowercaseKeys)

	if err := refuseLegacy(parsed); err != nil {
		return nil, err
	}
	root, directory, err := resolveStorage(parsed.Section(storageSection), home)
	if err != nil {
		return nil, err
	}
	return &Config{
		root:      root,
		directory: directory,
		allow:     keySet(parsed.Section(syncSection)),
		ignore:    keySet(parsed.Section(ignoreSection)),
	}, nil
}

// homeDirectory returns the home directory every other path here is resolved
// against.
//
// appspec/03: HOME "must be set for the program to function; if unset,
// home-relative operations fail with an uncaught error (nonzero exit)" -- the
// unguarded regime. A relative HOME is refused for the same reason and in the
// same regime: it is not a home directory, and accepting one would make every
// path the program later writes to depend on the working directory it happened
// to be started in.
func homeDirectory(env Environment) (string, error) {
	if env.Home == "" {
		return "", fault.Unguardedf("HOME is not set, so no home-relative path can be resolved")
	}
	if !filepath.IsAbs(env.Home) {
		return "", fault.Unguardedf("HOME is %q, which is not an absolute path", env.Home)
	}
	return filepath.Clean(env.Home), nil
}

// configPath returns the config file to read: the explicitly named one, or the
// first discovery candidate that exists.
func configPath(override Override, env Environment, home string) (string, error) {
	if !override.Set {
		return discover(env, home), nil
	}
	// appspec/03 "Explicit override": a leading "~" expands to the home
	// directory, and a RELATIVE path resolves against the home directory
	// rather than the working directory -- so `-c foo.cfg` means `~/foo.cfg`.
	// That second rule is the surprising one, and it is what makes an
	// explicitly named config subject to the containment check at all.
	path := homepath.Expand(override.Path, home)
	if !filepath.IsAbs(path) {
		path = filepath.Join(home, path)
	}
	path = filepath.Clean(path)
	if !existsAsFile(path) {
		return "", fault.Guardedf("The config file '%s' does not exist. Aborting.", path)
	}
	return path, nil
}

// discover applies the three-candidate precedence of appspec/03 "Discovery and
// precedence", returning the first candidate that exists as a regular file.
//
// The order is the specification's and the observable facts it lists follow
// from it: ~/.mackup.cfg "always wins if present", and with no ~/.mackup.cfg
// an existing MACKUP_CONFIG beats the XDG candidate because it is checked
// first.
//
// When no candidate exists, the default path is returned anyway -- as a
// nonexistent file, which appspec/03 says "parses as empty". So a machine with
// no config at all still produces a Config, with every default applied, and
// then fails at storage resolution if the default Dropbox engine finds
// nothing.
func discover(env Environment, home string) string {
	fallback := filepath.Join(home, defaultConfigName)

	candidates := []string{fallback}
	// Skipped when unset OR empty, which appspec/03 states outright. An empty
	// variable would otherwise expand to the working directory.
	if env.MackupConfig != "" {
		candidates = append(candidates, homepath.Absolute(homepath.Expand(env.MackupConfig, home)))
	}
	// The XDG base is resolved by the rule appspec/03 shares with appspec/05,
	// so the directory this program looks for a config in and the one the
	// application database relativizes definition paths against are the same
	// directory -- see internal/homepath.
	candidates = append(candidates, filepath.Join(homepath.ConfigHome(env.XDGConfigHome, home), xdgConfigName))

	for _, candidate := range candidates {
		if existsAsFile(candidate) {
			return candidate
		}
	}
	return fallback
}

// readConfig returns the config file's text, or the empty string when the file
// is not there.
//
// appspec/03 "Absent / empty": "A nonexistent or empty config file is
// structurally valid and parses to 'no sections set.'" So an absent file is
// not an error at all -- it is the ordinary state of a machine that has not
// been configured yet, and it produces every default.
//
// A file that exists and cannot be READ is a different thing and is not
// silently treated as empty: doing so would apply the Dropbox default to a
// machine whose config selects some other engine, and act on a configuration
// the user did not write. appspec/07 gives it no row, so it takes the
// unguarded shape its neighbours in that column take, naming the path.
func readConfig(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fault.Unguardedf("the config file '%s' cannot be read: %s", path, err)
	}
	return string(content), nil
}

// refuseLegacy implements appspec/03 "Legacy config rejection".
//
// Presence of either pre-migration section name aborts the program during
// config load, so it blocks every command. It is a GUARDED failure -- a clean
// multi-line message and exit 1 -- and the message says what appspec/03 says
// it says: an old config format was detected, and the program will do nothing
// rather than act incorrectly.
//
// Checked before anything else in the file is read, including the storage
// section: the whole point is that this file's contents cannot be trusted to
// mean what they appear to mean.
func refuseLegacy(parsed *ini.File) error {
	var found []string
	for _, name := range []string{legacyAllowedSection, legacyIgnoredSection} {
		if parsed.Has(name) {
			found = append(found, "["+name+"]")
		}
	}
	if len(found) == 0 {
		return nil
	}
	return fault.GuardedBlock(
		"Your config file uses the old Mackup configuration format: " + strings.Join(found, " and ") + ".\n" +
			"Those sections are now called [applications_to_sync] and [applications_to_ignore].\n" +
			"Doing nothing rather than acting on a configuration that would be read incorrectly.")
}

// resolveStorage reads the [storage] section and resolves the storage root and
// sub-directory from it.
//
// Every failure here is UNGUARDED -- appspec/07 puts an unknown engine, a
// file_system engine with no path, and a forbidden directory value together in
// that column -- except the engine's own inability to find its folder, which
// internal/storage raises as a guarded one. So this function's own errors and
// the ones it passes through are deliberately in different regimes.
func resolveStorage(section *ini.Section, home string) (root, directory string, err error) {
	engine := storage.Dropbox
	if value, present := section.Value(engineKey); present {
		named, known := storage.EngineNamed(value)
		if !known {
			// appspec/03 gives this diagnostic literally, so it is written
			// literally. The value is named, which is what appspec/02
			// requires of every unguarded row.
			return "", "", fault.Unguardedf("Unknown storage engine: %s", value)
		}
		engine = named
	}

	directory = defaultDirectory
	if value, present := section.Value(directoryKey); present {
		directory = value
	}
	if forbiddenDirectory(directory) {
		return "", "", fault.Unguardedf("the [storage] directory '%s' is not allowed: the storage sub-directory may never collide with a custom-apps directory", directory)
	}

	// The path is read for every engine and consulted by one. appspec/03 says
	// it is "ignored (not required) for the three auto-detected engines", so
	// a config that sets both an auto-detected engine and a path is not an
	// error -- the path is simply not used.
	path, _ := section.Value(pathKey)

	root, err = storage.Resolve(engine, home, path)
	if err != nil {
		return "", "", err
	}
	return root, directory, nil
}

// forbiddenSubdirectories are the sub-directory values appspec/03 refuses
// outright.
//
// All three name a custom-application definition directory: ".mackup" is the
// legacy one, and the other two are the XDG one appspec/05 reads, written
// relative to the XDG base and relative to home. The rule behind them is one
// sentence of appspec/03 -- "the storage sub-directory may never collide with
// a custom-apps directory" -- and the reason is that the sync engine would
// otherwise copy the definitions that decide what it syncs.
var forbiddenSubdirectories = []string{
	".mackup",
	"mackup/applications",
	".config/mackup/applications",
}

// forbiddenDirectory reports whether a [storage] directory value is one
// appspec/03 forbids.
//
// The suffix rule is the specification's too -- "any path ending in
// /.config/mackup/applications" -- so a value that merely leads somewhere
// deeper to the same place is caught as well.
func forbiddenDirectory(directory string) bool {
	for _, forbidden := range forbiddenSubdirectories {
		if directory == forbidden {
			return true
		}
	}
	return strings.HasSuffix(directory, "/.config/mackup/applications")
}

// keySet turns a bare-key section into the set of application keys it lists.
//
// The keys arrive already lowercased from the parser, which is appspec/03's
// case policy: "config application-list keys are case-normalized; definition
// file paths are case-exact." Both halves matter, and this is the half that
// lets a user write "Vim" in their config and have it match the key "vim".
func keySet(section *ini.Section) map[string]bool {
	keys := section.Keys()
	if len(keys) == 0 {
		return nil
	}
	set := make(map[string]bool, len(keys))
	for _, key := range keys {
		set[key] = true
	}
	return set
}

// sortedKeys returns a set's members in ascending order.
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// existsAsFile reports whether a path is there and is a regular file.
//
// appspec/03's discovery says "the first that exists as a regular file", and
// the explicit -c rule says only that the file "must exist". They are read as
// one rule here: a directory named as a config file is not a config file, and
// reporting it as absent is both the specification's own wording for
// discovery and the only answer that keeps the two paths behaving alike.
//
// A symlink to a regular file passes, because Stat follows it -- the file the
// user pointed at is the file that gets read.
func existsAsFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
