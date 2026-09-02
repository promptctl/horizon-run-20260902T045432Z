// Package appdb assembles the application database of
// appspec/05-application-database.md: the map from application key to display
// name and home-relative file set that drives list, show, and which files each
// sync command operates on.
//
// It is step 3 of the startup pipeline (appspec/01 section 4) and runs for
// every command, so a definition this package refuses aborts list and show
// exactly as it aborts a sync command. Assembly is total, like config load
// before it: Assemble returns a fully-built database or an error, because the
// two properties appspec/05 says the result guarantees -- every path is
// home-relative, every path keeps its exact case -- are guarantees the sync
// engine of appspec/06 "relies on without re-checking". A partially-validated
// database would be a database whose consumers' safety argument is false.
//
// The three definition directories are read as three filesystems layered by
// filename, and the built-in one is not privileged: it arrives as an fs.FS from
// internal/catalog and is parsed by the same code that parses a user's
// directory. appspec/05 decides precedence "by filename" across all three, and a
// second path into the database would mean two implementations of that rule.
//
// # Two rejections, and one this package deliberately does not make
//
// appspec/05 names exactly two fatal conditions -- an absolute path in either
// path section, and an $XDG_CONFIG_HOME outside the home directory -- and calls
// them "load-bearing for the sync engine's safety" rather than input hygiene.
// Both are implemented here, in the unguarded regime appspec/07's error table
// puts them in.
//
// It does NOT reject a ".." element in a definition path, and that is a
// decision rather than an oversight. Such a path escapes the home directory
// just as an absolute one does, so the guarantee quoted above is weaker than
// its wording suggests -- but appspec/05 enumerates its rejections, appspec/00
// makes conformance with the reference the contract, and adding a third
// rejection would abort a run the reference completes. The exposure is recorded
// as macklebox-appdb-171 rather than closed by inventing contract; the shipped
// catalog carries no such path and internal/catalog's tests keep it that way.
package appdb

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/promptctl/macklebox/internal/catalog"
	"github.com/promptctl/macklebox/internal/fault"
	"github.com/promptctl/macklebox/internal/homepath"
	"github.com/promptctl/macklebox/internal/ini"
)

// extension is the suffix of a definition file: appspec/05 makes the basename
// without it the application key.
//
// The constant the program DECIDES with lives here rather than in
// internal/catalog, which has an unexported copy describing only what its own
// files are named. The suffix is a property of definition files everywhere --
// appspec/05 applies the same "*.cfg" to both user directories -- so it belongs
// to the package that reads all three.
const extension = ".cfg"

// The sections and the one key appspec/05 recognizes in a definition file.
// Matched by exact name, as appspec/03 requires of section names and as
// ini.ExactKeys requires of keys in this file kind.
const (
	applicationSection    = "application"
	nameKey               = "name"
	configurationFiles    = "configuration_files"
	xdgConfigurationFiles = "xdg_configuration_files"
)

// legacyDirectory is the highest-precedence definition directory, "~/.mackup/".
const legacyDirectory = ".mackup"

// xdgApplicationsDirectory is the second, written relative to the XDG config
// base: "$XDG_CONFIG_HOME/mackup/applications/".
var xdgApplicationsDirectory = filepath.Join("mackup", "applications")

// builtinOrigin names the built-in directory in a diagnostic. It is not a path:
// the definitions are embedded in the binary, because appspec/05's "directory of
// definition files that ships with the program" has no installed location here.
const builtinOrigin = "<built-in>"

// An Environment is the part of the process environment appspec/05 reads.
//
// It mirrors config.Environment rather than sharing it, for the reason
// config.Override mirrors cli.Options: assembling the database does not depend
// on how the config was resolved, and a package that named its inputs by
// importing another stage's type would be coupled to that stage's shape. The
// two overlap in HOME and $XDG_CONFIG_HOME because both specifications read
// them, and internal/homepath is where they are guaranteed to read them alike.
type Environment struct {
	// Home is $HOME, the directory every path in the database is relative to.
	Home string
	// XDGConfigHome is $XDG_CONFIG_HOME: the base of the XDG custom-apps
	// directory, and of every [xdg_configuration_files] entry.
	XDGConfigHome string
}

// EnvironmentFromOS reads the environment the process was started with.
func EnvironmentFromOS() Environment {
	return Environment{
		Home:          os.Getenv("HOME"),
		XDGConfigHome: os.Getenv("XDG_CONFIG_HOME"),
	}
}

// An application is one assembled entry: the two lookups appspec/05 exposes per
// key, with the two authoring sources already merged into the one file set a
// consumer sees.
type application struct {
	name  string
	files []string
}

// A Database is the assembled application database: appspec/05's "map
// application key -> (display name, home-relative file set) with no duplicate
// keys".
//
// The fields are unexported and there are no setters. appspec/01 section 7 has
// the database re-assembled mid-run in one place -- the no-argument link path
// reloads it after linking the user's own Mackup config -- and that means
// building a second Database from Assemble, not mutating this one.
type Database struct {
	keys []string
	apps map[string]application
}

// Keys returns every application key in the database, sorted ascending.
//
// appspec/05 calls this "the set of all keys", and a set has no order of its
// own; the order returned is the one enumeration asks for, so that `list`
// prints what it is given rather than sorting it again and so that a
// diagnostic naming "the first offending definition" means the same thing on
// two machines.
//
// A copy, so a caller that sorts or filters what it gets -- config.Scope is
// handed exactly this slice -- cannot reorder the database underneath the next
// one.
func (d *Database) Keys() []string {
	keys := make([]string, len(d.keys))
	copy(keys, d.keys)
	return keys
}

// Name returns an application's display name, and whether the key is known.
//
// appspec/05: the name is "used nowhere for matching". The second result is what
// separates an unknown key from one whose definition wrote `name =`, which is an
// empty display name and not a missing application.
func (d *Database) Name(key string) (string, bool) {
	app, ok := d.apps[key]
	return app.name, ok
}

// Files returns an application's file set as home-relative paths sorted
// ascending, and whether the key is known.
//
// The union appspec/05 describes: "The two sections are two authoring sources
// for one file set; a consumer cannot tell an XDG-sourced path from a plain one
// -- they are one uniform home-relative type." A known application with no path
// sections has an empty file set, which appspec/05 makes valid and which is not
// the same answer as an unknown key.
func (d *Database) Files(key string) ([]string, bool) {
	app, ok := d.apps[key]
	if !ok {
		return nil, false
	}
	files := make([]string, len(app.files))
	copy(files, app.files)
	return files, true
}

// Assemble reads the three definition directories and builds the database.
//
// The order is appspec/05's: the XDG base is resolved and checked first, since
// it decides where one of the three directories is as well as what every
// [xdg_configuration_files] entry means; then filenames are collected in
// precedence order; then the winning file per key is read and validated.
//
// Only winners are read. appspec/05 says a shadowed definition is "not read at
// all for that key", which is observable and not an optimization: a definition
// in the XDG directory that would be refused for an absolute path does not
// refuse anything when ~/.mackup holds a file of the same name.
func Assemble(env Environment) (*Database, error) {
	home, err := homeDirectory(env)
	if err != nil {
		return nil, err
	}
	xdgBase, err := configBase(env, home)
	if err != nil {
		return nil, err
	}

	winners := discover(home, xdgBase)
	keys := make([]string, 0, len(winners))
	for key := range winners {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	// Sorted before the files are read, so that a directory holding two
	// refusable definitions reports the same one on every run. Nothing in
	// appspec/05 chooses between them, and a diagnostic that depended on map
	// iteration order would be a contract nothing could hold the program to.
	apps := make(map[string]application, len(keys))
	for _, key := range keys {
		app, err := read(winners[key], home, xdgBase)
		if err != nil {
			return nil, err
		}
		apps[key] = app
	}
	return &Database{keys: keys, apps: apps}, nil
}

// homeDirectory returns the home directory every path in the database is
// resolved against.
//
// appspec/03 makes HOME required "for the program to function" and puts its
// absence in the unguarded regime; this stage cannot do anything home-relative
// without it either. In the pipeline the condition is unreachable -- config load
// is step 2 and refuses first -- and the guard is here anyway, because a package
// whose correctness depends on the order its caller happens to run stages in has
// no contract of its own to test.
func homeDirectory(env Environment) (string, error) {
	if env.Home == "" {
		return "", fault.Unguardedf("HOME is not set, so no home-relative path can be resolved")
	}
	if !filepath.IsAbs(env.Home) {
		return "", fault.Unguardedf("HOME is %q, which is not an absolute path", env.Home)
	}
	return filepath.Clean(env.Home), nil
}

// configBase resolves the XDG config directory and applies appspec/05's
// containment rule to it.
//
// "If $XDG_CONFIG_HOME resolves to a location not within the home directory,
// database assembly fails with a fatal (uncaught) error stating that
// $XDG_CONFIG_HOME must be somewhere within the home directory, nonzero exit."
//
// Checked once, up front, and not lazily when the first XDG entry is
// relativized. appspec/05 states the consequence as unconditional -- "this check
// fires while assembling the database, so it blocks every command" -- and a
// lazy check makes that true only for as long as some definition happens to
// carry an [xdg_configuration_files] section. It is also the base of one of the
// three directories being read, so assembly needs it resolved either way.
//
// The diagnostic names the RESOLVED base rather than the raw variable, which is
// what appspec/02 asks of an unguarded failure ("naming the offending value")
// and is the more useful of the two: a relative $XDG_CONFIG_HOME is outside home
// because of where the process was started, and the raw value does not show
// that.
func configBase(env Environment, home string) (string, error) {
	base := homepath.ConfigHome(env.XDGConfigHome, home)
	if !homepath.Inside(base, home) {
		return "", fault.Unguardedf("$XDG_CONFIG_HOME must be somewhere within your home directory: %s", base)
	}
	return base, nil
}

// A candidate is one definition file that won its key, and where it came from.
//
// origin is carried for diagnostics only. Two of the three sources are
// directories and one is embedded in the binary, so it is a display string and
// not a path something opens.
type candidate struct {
	files  fs.FS
	name   string
	origin string
}

// discover applies the three-tier precedence of appspec/05 "Discovery", mapping
// each application key to the definition file that wins it.
//
// "One definition file wins per application key, by a fixed three-tier
// precedence decided by filename": ~/.mackup/ first, then the XDG custom-apps
// directory, then the built-in set, each taken only where the key is not
// already claimed. The three are read as filesystems so that the built-in
// directory -- which is embedded, not installed -- is layered by the same code
// as the two on disk.
func discover(home, xdgBase string) map[string]candidate {
	winners := map[string]candidate{}
	legacyPath := filepath.Join(home, legacyDirectory)
	xdgPath := filepath.Join(xdgBase, xdgApplicationsDirectory)

	for _, source := range []candidate{
		{files: os.DirFS(legacyPath), origin: legacyPath},
		{files: os.DirFS(xdgPath), origin: xdgPath},
		{files: catalog.Definitions(), origin: builtinOrigin},
	} {
		for _, name := range definitionFiles(source.files) {
			key := strings.TrimSuffix(name, extension)
			if _, taken := winners[key]; taken {
				continue
			}
			winners[key] = candidate{files: source.files, name: name, origin: source.origin}
		}
	}
	return winners
}

// definitionFiles lists the definition files directly in one source, in no
// particular order.
//
// Three rules of appspec/05 "Discovery", each stated there:
//
//   - "Every *.cfg file DIRECTLY in this directory is taken" -- so a definition
//     inside a sub-directory is not one, and neither is a directory whose own
//     name ends in .cfg. The name must be a regular file, which a symlink to one
//     is: fs.Stat follows it, matching how appspec/03's discovery reads a config
//     candidate.
//   - "Only files ending in .cfg are considered; other files are ignored." The
//     suffix is compared exactly, so a file named .CFG is one of the others.
//   - "A user directory that does not exist is simply skipped." Any error
//     listing the directory is skipped for the same reason, since neither
//     specification gives a row to a definition directory that cannot be read
//     and the two user directories are optional by construction.
//
// A name beginning with "." is not a definition, which is the specification's
// own "*.cfg" glob read as a glob: it matches neither ".cfg" itself -- whose key
// would be the empty string -- nor a dotfile beside a real definition. The
// built-in directory already behaves this way, because go:embed without the
// "all:" prefix skips such names, so the rule is also what keeps the three
// sources comparable on the footing precedence needs. It earns itself in a user
// directory: copying ~/.mackup to a non-native volume and back leaves
// AppleDouble files named "._vim.cfg", and a phantom "._vim" application is
// exactly the silent kind of wrong.
func definitionFiles(files fs.FS) []string {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, extension) {
			continue
		}
		info, err := fs.Stat(files, name)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		names = append(names, name)
	}
	return names
}

// read parses one winning definition file and validates it.
func read(winner candidate, home, xdgBase string) (application, error) {
	origin := filepath.Join(winner.origin, winner.name)
	content, err := fs.ReadFile(winner.files, winner.name)
	if err != nil {
		// appspec/07 gives no row to a definition file that cannot be read, so
		// it takes the unguarded shape its neighbours in that column take and
		// names the file. Treating it as empty is the one thing it must not do:
		// that would silently drop an application the user's own directory
		// claims, or -- for a shadowed built-in -- resurrect the definition the
		// user's file was written to replace.
		return application{}, fault.Unguardedf("the definition file '%s' cannot be read: %s", origin, err)
	}

	// ExactKeys, which is appspec/05's half of the case-policy pair: "Key names
	// here are case-preserving (they are not lowercased, unlike the user-config
	// application lists in 03) so paths keep their exact case." It applies to
	// the whole file, so the [application] "name" key is matched exactly too --
	// see ini.ExactKeys, where that reading is written down.
	parsed := ini.Parse(string(content), ini.ExactKeys)

	name, present := parsed.Section(applicationSection).Value(nameKey)
	if !present {
		// appspec/05 makes the section and key "required. ... (each part of the
		// contract)" without giving the failure a row in appspec/07's table, so
		// it takes the unguarded shape and names the file. The alternative --
		// defaulting the display name to the key -- would let a definition the
		// specification calls invalid pass silently, and `show` would print a
		// name the user never wrote.
		return application{}, fault.Unguardedf("the definition file '%s' has no [%s] %s", origin, applicationSection, nameKey)
	}

	files := map[string]bool{}
	for _, path := range parsed.Section(configurationFiles).Keys() {
		if err := refuseAbsolute(path); err != nil {
			return application{}, err
		}
		// Stored exactly as written. appspec/05's case-exactness is the point,
		// and cleaning would also rewrite a path the sync engine is meant to
		// join to HOME verbatim.
		files[path] = true
	}
	for _, path := range parsed.Section(xdgConfigurationFiles).Keys() {
		if err := refuseAbsolute(path); err != nil {
			return application{}, err
		}
		files[relativize(path, home, xdgBase)] = true
	}

	// Deduplicated only after the XDG entries are home-relativized, because
	// before that they are not comparable: "git/config" in
	// [xdg_configuration_files] is ~/.config/git/config, and "git/config" in
	// [configuration_files] is ~/git/config. Several shipped definitions carry
	// exactly that pair.
	set := make([]string, 0, len(files))
	for path := range files {
		set = append(set, path)
	}
	sort.Strings(set)
	return application{name: name, files: set}, nil
}

// refuseAbsolute implements appspec/05's absolute-path rejection, which applies
// to both path sections alike.
//
// "A path that starts with '/' (an absolute path) is rejected: assembling the
// database fails with a fatal (uncaught) error naming the offending path
// (`Unsupported absolute path: <path>`), nonzero exit." The sentence is
// reproduced literally because appspec/05 gives it literally; the unguarded
// prefix internal/fault adds is this program's way of keeping appspec/01
// section 6's two regimes apart, and it leaves the sentence contiguous.
//
// The test is the leading '/' the specification names, not filepath.IsAbs. They
// agree on every platform this program targets, and where they would not, the
// specification's own wording is the contract.
func refuseAbsolute(path string) error {
	if strings.HasPrefix(path, "/") {
		return fault.Unguardedf("Unsupported absolute path: %s", path)
	}
	return nil
}

// relativize turns an [xdg_configuration_files] entry into the home-relative
// path the database stores.
//
// appspec/05: "For each such path p, the effective config item is
// $XDG_CONFIG_HOME/p rendered as a home-relative path -- i.e. the XDG base is
// joined to p, then the home prefix is stripped so the item is stored/looked-up
// relative to home just like a [configuration_files] entry."
//
// The base is inside the home directory by the time this runs, so the strip
// cannot fail for a path with no ".." of its own; filepath.Rel's error is
// impossible on two absolute paths and the joined path is returned unchanged if
// it ever were, which keeps a would-be panic out of a stage that is meant to
// either build a database or report why it could not.
func relativize(path, home, xdgBase string) string {
	joined := filepath.Join(xdgBase, path)
	relative, err := filepath.Rel(home, joined)
	if err != nil {
		return joined
	}
	return relative
}
