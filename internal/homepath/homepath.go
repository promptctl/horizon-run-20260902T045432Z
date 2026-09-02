// Package homepath holds the home-relative path rules that
// appspec/03-configuration.md and appspec/05-application-database.md share.
//
// Both specifications resolve paths against the home directory, expand a
// leading "~", derive the XDG config base from $XDG_CONFIG_HOME with the same
// default, and ask whether a resolved path lies inside the home directory.
// appspec/03 asks it of the config file it is about to read; appspec/05 asks
// it of the XDG base it is about to relativize definition paths against. They
// are one rule read by two stages, so they are written once here.
//
// The alternative was a copy in each package, and the failure it invites is
// specific rather than stylistic: the two would answer differently for a
// relative $XDG_CONFIG_HOME, or for a "~" that one expands and the other does
// not, and the program would then read its config from one directory while
// resolving definitions against another. Nothing in either specification
// describes that state, so nothing would report it.
//
// Nothing here consults the environment or the filesystem. These are string
// rules over paths a caller has already obtained, which is what lets both
// packages state the environment a test case is about instead of mutating the
// process's own. Require is no exception: it is handed the value of $HOME, not
// asked to go and read it.
//
// It does depend on internal/fault, because the failure regime and the wording
// of a diagnostic travel with the rule that produces them. Two stages printing
// two different sentences for one unset variable is the same divergence as two
// stages disagreeing about a path, and appspec/03 states that failure once.
package homepath

import (
	"path/filepath"
	"strings"

	"github.com/promptctl/macklebox/internal/fault"
)

// Require validates the home directory every other rule here resolves against,
// returning it cleaned.
//
// appspec/03 makes $HOME required "for the program to function" and puts its
// absence in the unguarded regime. A relative value is refused in the same
// regime and for the same reason: it is not a home directory, and accepting one
// would make every path the program later resolves depend on the working
// directory it happened to be started in.
//
// Both the config load of appspec/03 and the database assembly of appspec/05
// begin here. The second is unreachable in the pipeline -- config load is step 2
// and refuses first -- and calls it anyway, because a package whose correctness
// depends on the order its caller happens to run stages in has no contract of
// its own to test.
func Require(home string) (string, error) {
	if home == "" {
		return "", fault.Unguardedf("HOME is not set, so no home-relative path can be resolved")
	}
	if !filepath.IsAbs(home) {
		return "", fault.Unguardedf("HOME is %q, which is not an absolute path", home)
	}
	return filepath.Clean(home), nil
}

// xdgConfigDefault is the XDG config base used when $XDG_CONFIG_HOME is unset,
// written home-relative. appspec/03 and appspec/05 both give it: "$XDG_CONFIG_HOME
// defaults to ~/.config when unset."
const xdgConfigDefault = ".config"

// Expand replaces a leading "~" with the home directory.
//
// appspec/03 says exactly that and no more, so "~otheruser/x" is NOT expanded
// to another user's home: it stays a relative path whose first element happens
// to begin with a tilde, and is resolved against the home or working directory
// by the caller like any other relative path. Expanding it would mean reading
// the password database to produce a path neither specification mentions.
func Expand(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

// Absolute makes a path absolute against the working directory, leaving an
// already-absolute one alone.
//
// It applies to the paths the two specifications take from the environment.
// Neither says what a relative $MACKUP_CONFIG or $XDG_CONFIG_HOME means, so
// this neither invents the home-relative rule that appspec/03 gives the -c
// option nor leaves a relative value to be compared against the home directory
// as though it were absolute -- which is how a containment check silently
// passes.
func Absolute(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.Abs(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// Inside reports whether a resolved path lies within the home directory.
//
// The comparison is lexical, on cleaned absolute paths, and deliberately does
// not resolve symlinks. appspec/03 states its check as being about the resolved
// config PATH -- "the finally-resolved config path must lie inside the home
// directory" -- and a check that followed links would refuse a home directory
// that is itself a symlink, which is an ordinary arrangement. appspec/05's
// check on the XDG base is worded the same way and is read the same way.
//
// A path equal to the home directory counts as inside it. That case cannot
// arise for a config file, which must be a regular file, but it can for an XDG
// base, and the predicate should not claim otherwise.
func Inside(path, home string) bool {
	relative, err := filepath.Rel(home, path)
	if err != nil {
		return false
	}
	// The escape is the path ELEMENT "..", not the prefix: a file named
	// "~/..config" produces the relative path "..config", which a HasPrefix on
	// ".." rejects as outside the home directory it is plainly inside. Dotted
	// names are what this program manages, so that is not a corner.
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// ConfigHome resolves the XDG config base from the value of $XDG_CONFIG_HOME,
// applying the default both specifications give it and returning an absolute,
// cleaned path.
//
// An empty value is the unset one: appspec/03 and appspec/05 agree that
// "$XDG_CONFIG_HOME defaults to ~/.config when unset", and an empty variable
// would otherwise expand to the working directory. A value that IS set is
// tilde-expanded and then made absolute, so that every caller compares and
// joins the same string.
//
// It does not check that the result is inside the home directory. That check is
// appspec/05's and fires while the application database is assembled; appspec/03
// deliberately does not make it of the config candidate, whose containment is
// checked on the finally-resolved config path instead. Answering both here
// would move a failure to a stage neither specification puts it in.
func ConfigHome(xdgConfigHome, home string) string {
	if xdgConfigHome == "" {
		return filepath.Join(home, xdgConfigDefault)
	}
	return Absolute(Expand(xdgConfigHome, home))
}
