// Package storage resolves the storage root -- the directory the Mackup
// folder is created inside -- as appspec/04-storage-engines.md specifies.
//
// appspec/04 opens by naming the shape it wants: "a closed enumeration of
// exactly four members ... a sealed type, where 'any other value' is
// unrepresentable (the 'unknown engine' case is the type-boundary rejection,
// 03), not a runtime string compare inside resolution." That is why Engine is
// an integer type built only by EngineNamed, and why no resolver here compares
// an engine name to anything. Once a value of this package's Engine type
// exists, the string it came from has already been accepted or refused.
//
// The three-clause interface contract is the other half. All four engines
// implement Resolver, whose single method takes nothing beyond the ambient
// environment the resolver was constructed with and returns one absolute path
// -- and whose SUCCESS POSTCONDITION IS DELIBERATELY NOT UNIFORM. The three
// auto-detecting engines return a directory that exists, or read a path out of
// a client database that does; FileSystem returns the user's string with no
// existence check at all. appspec/04 states the trap by name -- "A
// reimplementer must not 'add the missing check' to the user-path engine" --
// because the deferral is observable: with a nonexistent user path the failure
// must surface at the environment gate's "Unable to find the storage folder"
// message (appspec/01 section 4, macklebox-resolvers-5iw.4), not here.
package storage

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/promptctl/macklebox/internal/fault"
	"github.com/promptctl/macklebox/internal/sqlite"
)

// An Engine is one of the four storage engines of appspec/04.
//
// The zero value is Dropbox, which is also appspec/03's default when the
// config names no engine -- so a Config that was never given one resolves the
// way the specification says it should rather than by accident.
type Engine int

const (
	// Dropbox reads the Dropbox client's host database. appspec/03's default.
	Dropbox Engine = iota
	// GoogleDrive reads the Google Drive client's sync configuration database.
	GoogleDrive
	// ICloud is the fixed macOS iCloud Drive location.
	ICloud
	// FileSystem is a path the user supplies in the config.
	FileSystem
)

// engineNames maps each engine to the value appspec/03 spells it with in the
// config file. Names are matched exactly: appspec/03 states that "values in
// [storage] are case-sensitive".
var engineNames = map[Engine]string{
	Dropbox:     "dropbox",
	GoogleDrive: "google_drive",
	ICloud:      "icloud",
	FileSystem:  "file_system",
}

// String names an engine as the config file spells it.
func (e Engine) String() string {
	if name, ok := engineNames[e]; ok {
		return name
	}
	return fmt.Sprintf("Engine(%d)", int(e))
}

// EngineNamed returns the engine a config value names, and whether it names
// one at all.
//
// This is the type boundary appspec/04 asks for: it is the single place a
// string becomes an Engine, so an unrecognized value is rejected here and no
// resolver below ever meets one. The rejection itself belongs to the config
// (appspec/03: "any other value is a fatal error"), which is why this reports
// a bool rather than building the diagnostic.
func EngineNamed(value string) (Engine, bool) {
	for engine, name := range engineNames {
		if name == value {
			return engine, true
		}
	}
	return Dropbox, false
}

// A Resolver produces the storage root of one engine.
//
// Root takes no argument: appspec/04 clause 1 gives each engine "no per-call
// argument beyond ambient environment (HOME)", so everything an engine needs
// is supplied when it is constructed. What it returns is one absolute path,
// which for three of the four engines exists and for the fourth deliberately
// need not -- see the package comment, and Resolve.
type Resolver interface {
	Root() (string, error)
}

// Resolve builds the resolver for an engine and runs it, returning the storage
// root.
//
// path is the config's [storage] path value, which only FileSystem reads;
// appspec/03 says it is "ignored (not required) for the three auto-detected
// engines", so it is passed to all four and consulted by one.
//
// Resolution is eager, at config-load time: appspec/03's opening paragraph
// makes the resolved storage location one of the five properties a Config has
// at construction, and appspec/02 makes a failure here abort every command
// "including list and show, which otherwise touch no storage".
func Resolve(engine Engine, home, path string) (string, error) {
	return resolverFor(engine, home, path).Root()
}

// resolverFor builds the resolver for an engine.
//
// The switch is exhaustive over a closed type, so the default arm is
// unreachable while EngineNamed is the only way to obtain an Engine. It
// returns a resolver that fails rather than panicking: an engine added to the
// enumeration without a resolver is a programming error, and the honest
// response to one at runtime is a diagnostic naming it, not a crash inside a
// config load.
func resolverFor(engine Engine, home, path string) Resolver {
	switch engine {
	case Dropbox:
		return dropbox{home: home}
	case GoogleDrive:
		return googleDrive{home: home}
	case ICloud:
		return iCloud{home: home}
	case FileSystem:
		return fileSystem{home: home, path: path}
	default:
		return unresolvable{engine: engine}
	}
}

// documentationURL is the pointer appspec/04 requires the "unable to find your
// provider" message to carry.
const documentationURL = "https://github.com/promptctl/macklebox"

// unlocatable builds the multi-line diagnostic appspec/04 specifies for an
// auto-detecting engine that cannot find its folder: the "=(" line naming the
// provider, guidance to consider another one, and a documentation URL.
//
// It is guarded -- appspec/07's table puts "storage folder not locatable
// (dropbox/gdrive/icloud)" in the guarded column, exit 1 -- and it is one
// message for three engines on purpose. appspec/04 gives all three "a message
// of the same shape" and differs only in the {provider} slot, so writing it
// once is what keeps them the same shape.
//
// cause is not shown. The user cannot act on "the second token was not valid
// Base64" any more than on "the file was missing", the spec's message says
// nothing about it, and appspec/04 lists every distinct cause as producing
// this one message.
func unlocatable(provider string) *fault.Error {
	return fault.GuardedBlock(fmt.Sprintf(
		"Unable to find your %s =(\n"+
			"If you use another sync provider, set it in your .mackup.cfg [storage] section.\n"+
			"%s",
		provider, documentationURL))
}

// usableText reports whether a value read out of a third party's file can be
// used as a filesystem path.
//
// appspec/04 requires the Dropbox host database's decoded bytes to be
// "decodable to text" and the Google Drive value to be "present and
// non-empty". Both are the same question asked of bytes this program did not
// write, so it is answered once. A NUL is refused along with invalid UTF-8:
// no path may contain one, and a value carrying one is truncated silently by
// every system call it reaches.
func usableText(value string) bool {
	return value != "" && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

// dropbox resolves the Dropbox folder from the client's host database.
type dropbox struct{ home string }

// hostDB is where the Dropbox client keeps the host database appspec/04 reads.
const hostDB = ".dropbox/host.db"

// Root reads ~/.dropbox/host.db and decodes the Dropbox folder out of it.
//
// appspec/04 calls this data shape contract, "peer-observable": the file is
// read as whitespace-separated tokens, it must hold at least two, and the
// SECOND is strict Base64 whose decoded bytes are the absolute path.
//
// It does not check that the decoded path exists. appspec/04's clause 2 says
// the auto-detecting engines return a path that exists "or is read from an
// existing client database", and this engine is the second kind -- its list of
// failures is the file's, not the folder's. A Dropbox folder the user has
// since deleted therefore fails at the environment gate, with the same message
// a bad file_system path gets.
func (d dropbox) Root() (string, error) {
	content, err := os.ReadFile(filepath.Join(d.home, hostDB))
	if err != nil {
		return "", unlocatable("Dropbox install")
	}
	tokens := strings.Fields(string(content))
	if len(tokens) < 2 {
		return "", unlocatable("Dropbox install")
	}
	// Strict, so that a token with the wrong padding or a stray character is
	// refused rather than decoded to something shorter. appspec/04 says
	// "strict Base64" outright, and the value being decoded is a path the
	// program will later write files under.
	decoded, err := base64.StdEncoding.Strict().DecodeString(tokens[1])
	if err != nil {
		return "", unlocatable("Dropbox install")
	}
	root := string(decoded)
	if !usableText(root) {
		return "", unlocatable("Dropbox install")
	}
	return root, nil
}

// googleDrive resolves the Google Drive folder from the client's sync
// configuration database.
type googleDrive struct{ home string }

// The Google Drive client's database, and the row inside it that holds the
// local sync root. appspec/04 gives the preferred path, the fallback, and the
// query; all four values are that specification's, not this program's.
const (
	driveSupportDir = "Library/Application Support/Google/Drive"
	drivePreferred  = "user_default/sync_config.db"
	driveFallback   = "sync_config.db"

	driveTable       = "data"
	driveKeyColumn   = "entry_key"
	driveKey         = "local_sync_root_path"
	driveValueColumn = "data_value"
)

// Root finds whichever of the client's two database paths exists and reads the
// local sync root out of it.
//
// appspec/04 prefers the user_default database "if it exists" and otherwise
// takes the one beside it, so the choice is by existence and not by which
// query succeeds: a present-but-unreadable preferred database is a failure,
// not a reason to fall back. Every failure -- neither file present, the file
// unreadable, the row absent, the value empty -- is the one message appspec/04
// gives this engine.
func (g googleDrive) Root() (string, error) {
	support := filepath.Join(g.home, driveSupportDir)
	var db string
	for _, candidate := range []string{
		filepath.Join(support, drivePreferred),
		filepath.Join(support, driveFallback),
	} {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			db = candidate
			break
		}
	}
	if db == "" {
		return "", unlocatable("Google Drive install")
	}
	root, err := sqlite.Lookup(db, driveTable, driveKeyColumn, driveKey, driveValueColumn)
	if err != nil || !usableText(root) {
		return "", unlocatable("Google Drive install")
	}
	return root, nil
}

// iCloud resolves the iCloud Drive folder, which is at a fixed location.
type iCloud struct{ home string }

// iCloudDir is the home-relative iCloud Drive path appspec/04 fixes.
const iCloudDir = "Library/Mobile Documents/com~apple~CloudDocs"

// Root reports the iCloud Drive directory if it exists.
//
// appspec/04: "This engine requires no reading of any client database -- the
// existence of that directory *is* the resolution." So the stat is not a
// validation added on top of the resolution; it is the whole of it, and a
// symlink to a directory counts, which is why os.Stat is used rather than
// Lstat.
func (i iCloud) Root() (string, error) {
	root := filepath.Join(i.home, iCloudDir)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", unlocatable("iCloud Drive")
	}
	return root, nil
}

// fileSystem is the storage root the user names in the config.
type fileSystem struct{ home, path string }

// Root resolves the configured path, without checking that anything is there.
//
// The two rules are appspec/03's: an absolute path is used verbatim, and a
// relative one is resolved under the home directory, so `path = some/folder`
// means `~/some/folder`. Nothing is quoted or unquoted -- appspec/03 says a
// path containing spaces "needs no quoting or escaping", so the value arrives
// here exactly as the user typed it after the config parser trimmed the line.
//
// THE MISSING EXISTENCE CHECK IS THE CONTRACT. appspec/04 clause 2 names this
// engine as the one whose postcondition differs and tells a reimplementer not
// to "fix" it: the uniform guarantee is supplied later by the environment
// gate, and the deferral is observable, because a nonexistent path must fail
// with "Unable to find the storage folder: <path>" at the gate rather than
// with this engine's message at load time. Adding a stat here would move a
// message the specification places elsewhere.
//
// An unset path is the one failure this engine has, and appspec/04 clause 3
// singles it out as the exception to the guarded shape its three siblings use:
// it is "an uncaught config error", the unguarded regime of appspec/01
// section 6.
func (f fileSystem) Root() (string, error) {
	if f.path == "" {
		return "", fault.Unguardedf("the %s storage engine needs a [storage] path, and the config file sets none", FileSystem)
	}
	if filepath.IsAbs(f.path) {
		return f.path, nil
	}
	return filepath.Join(f.home, f.path), nil
}

// unresolvable stands in for an Engine value with no resolver.
type unresolvable struct{ engine Engine }

// Root reports that the program has an engine it does not know how to resolve.
//
// Unreachable while resolverFor covers the enumeration; it exists so that
// adding a member and forgetting its arm produces a named diagnostic during a
// config load rather than a nil dereference.
func (u unresolvable) Root() (string, error) {
	return "", fault.Unguardedf("no resolver is built for the %s storage engine", u.engine)
}
