// Package catalog ships the built-in application definitions -- the third and
// lowest-precedence of the three definition directories in
// appspec/05-application-database.md, "the directory of definition files that
// ships with the program".
//
// It holds data and the means to read it, and nothing else. The definition
// format, the discovery and precedence rules that layer the two user
// directories over this one, and the enumeration lookups all belong to the
// database itself (macklebox-resolvers-5iw.3); a consumer here gets a
// filesystem of .cfg files and parses them the same way it parses a user's.
// That is deliberate: the built-in set must not be a second, privileged path
// into the database, or the precedence rules would have two implementations
// to agree with each other.
//
// Provenance. This is an MIT-licensed clean-room reimplementation. The
// application KEYS are project data, taken from
// appspec/appendix-application-names.md, which records them and nothing else:
// the specification deliberately does not record display names or file sets,
// and they cannot be recovered from it. Every display name and every path in
// these files was therefore authored for this project from general knowledge
// of where each program keeps its configuration. No definition data was
// copied from the reference implementation or from any other third-party
// source, whose data files carry their own licenses.
//
// A definition with an empty file set is valid (appspec/05: "A definition
// with neither contributes an application that has an empty file set (it
// still appears in list and show)"), so a key whose paths are not yet
// authored still lists and shows correctly. Coverage of file sets grows by
// editing these files; the key set is fixed by the appendix and pinned by
// TestTheShippedKeysAreExactlyTheAppendix.
package catalog

import (
	"embed"
	"io/fs"
)

// extension is the suffix of a definition file. appspec/05 makes the basename
// without it the application key.
//
// Unexported on purpose. The suffix is a property of definition files
// everywhere, not of this directory -- appspec/05 applies it to both user
// directories too -- so the database that reads all three owns the constant
// the program decides with. This copy is only what these files are named, and
// exporting it would invite the database to import a data package for a
// string.
const extension = ".cfg"

// embedded carries the definition directory into the binary. The program has
// no install step that could place a directory beside it, so "ships with the
// program" means "is in the program" here.
//
// The pattern names the directory rather than applications/*.cfg, so a file
// added there under some other name is embedded too and
// TestEveryShippedFileIsADefinition sees it. Embedding only *.cfg would hide
// a mis-named definition from the very check that exists to catch one.
//
// A directory pattern without the all: prefix still skips names beginning
// with "." or "_", and that exclusion is wanted rather than tolerated: it is
// what keeps a Finder-dropped .DS_Store or an editor lock file out of the
// binary, where it would fail that check on a machine whose only fault was
// having opened the directory. The names it hides are not names a definition
// can have -- the basename is the application key, and no key in the appendix
// starts with either character.
//
//go:embed applications
var embedded embed.FS

// definitions is embedded rooted at the definition directory, so an entry is
// "<key>.cfg" rather than "applications/<key>.cfg". Resolved once at init:
// fs.Sub can only fail on a malformed path, and the path here is a constant.
var definitions = func() fs.FS {
	sub, err := fs.Sub(embedded, "applications")
	if err != nil {
		panic("catalog: the embedded definition directory is unreachable: " + err.Error())
	}
	return sub
}()

// Definitions returns the built-in definition files as a filesystem whose
// entries are named "<key>.cfg".
//
// An fs.FS rather than a map of parsed definitions, because appspec/05 decides
// precedence "by filename" across three directories: the built-in set has to
// be comparable with a user directory on that footing, and a caller that
// already walks ~/.mackup can walk this with the same code.
func Definitions() fs.FS { return definitions }
