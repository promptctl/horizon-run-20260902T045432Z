// Package plist reads Apple property lists into a comparable value model.
//
// It exists for one clause of appspec/06-sync-operations.md "Drift detection":
// when the two regular files being compared "both parse as property-list
// (plist) files", they are "compared by parsed content; identical if equal,
// else a unified diff of their pretty-printed structures". Comparing parsed
// content is the whole point of that clause -- two plists holding the same
// settings are the same configuration however they happen to be encoded on
// disk, and a byte comparison of them reports drift that the user cannot act
// on and did not cause.
//
// That is also why BOTH on-disk formats are read here. A preference file
// written by macOS is a binary plist far more often than an XML one --
// `defaults write` and CFPreferences produce binary -- so a reader that
// understood only the XML spelling would send the format the specification is
// mostly about down appspec/06's byte-for-byte arm and print "binary contents
// differ" for a one-key change. The two formats are alternative encodings of
// one value model, and this package returns that model rather than either
// encoding.
//
// # What a caller gets back
//
// Parse returns the property list as ordinary Go values, one per plist type:
//
//	<dict>     map[string]any
//	<array>    []any
//	<string>   string
//	<integer>  int64
//	<real>     float64
//	<true/>    bool
//	<false/>   bool
//	<date>     time.Time
//	<data>     []byte
//
// Format renders that model as the "pretty-printed structure" the drift diff
// is taken over. The two are a pair and the pairing is a contract: equal
// renderings mean equal content, so a caller may decide "identical" by
// comparing renderings rather than by walking the values. See Format.
//
// # What it deliberately refuses
//
// Two binary-plist object kinds are rejected rather than modelled: the UID
// (marker 0x8, produced by NSKeyedArchiver) and the set (marker 0xC). Neither
// has an XML spelling, so no XML/binary pair can agree about one, and both
// occur in archived object graphs rather than in the editable configuration
// this program syncs -- a structural diff of a keyed archive is not more
// useful to the user than the "binary contents differ" note appspec/06 already
// gives that case. Rejecting them keeps this value model exactly the set of
// types the XML format can express, which is what lets the two formats be
// compared against each other at all. A file holding one falls through to
// appspec/06's byte-for-byte arm, which is the honest answer and not a
// silent one.
//
// The legacy OpenStep/ASCII plist spelling is not read either. It is not
// written by any current macOS API, and, unlike the two kinds above, accepting
// it would mean guessing: its syntax has no magic number and overlaps ordinary
// text config files, so a permissive reader would start classifying arbitrary
// files as plists and diffing them structurally. appspec/06 asks whether a
// file parses AS a plist, and a format that cannot be recognised cannot answer
// that question safely.
package plist

import (
	"bytes"
	"errors"
	"fmt"
)

// maxDepth bounds how deeply a property list may nest.
//
// Both readers below recurse once per level, so without a bound a file made of
// nothing but open brackets recurses until the goroutine's stack is exhausted
// -- which is a fatal error in Go, not a returned one, so it takes the whole
// sync run down rather than falling through to appspec/06's byte-for-byte arm.
// A file that deep is not a property list anyone wrote: CoreFoundation's own
// XML writer indents one level per nesting and macOS preference files are a
// handful deep, so this is two orders of magnitude above anything real and is
// a crash guard rather than a limit a caller can meet.
//
// It is the count of containers, and exactly that many are allowed: a document
// nested maxDepth deep parses and one nested deeper does not. Both readers
// spell the test as ">=" for that reason, each against its own count -- the
// XML reader's depth argument and the binary reader's set of open objects --
// and a case builds both shapes at the boundary.
const maxDepth = 512

// binaryMagic is the eight-byte header every binary property list opens with.
// The version digits are part of it: CoreFoundation writes "bplist00" and this
// reader implements that version.
var binaryMagic = []byte("bplist00")

// ErrNotAPlist reports that the bytes are not a property list this package
// reads.
//
// It is a distinct error because appspec/06's plist arm is a CLASSIFICATION,
// not a validation: a caller asks whether the two files "both parse as plist
// files" and moves on to the text and byte arms when either does not. That is
// an ordinary outcome for most of the files this program syncs -- an .ackrc is
// not a plist and nothing is wrong -- so it must be distinguishable from a
// plist that is genuinely malformed. Callers that only need the classification
// can ignore the distinction and treat any error as "not comparable as a
// plist", which is what drift does.
var ErrNotAPlist = errors.New("not a property list")

// Parse reads a property list from data in either the binary or the XML
// format, choosing between them by what the bytes begin with.
//
// The binary format is detected by its magic number, which is unambiguous. The
// XML format has none, so everything else is offered to the XML reader, which
// requires a <plist> document element and rejects anything that is not one --
// including ordinary XML that happens to be well-formed. That asymmetry is
// deliberate: the question appspec/06 asks is whether a file IS a plist, and a
// reader that answered yes for any parseable XML would take a text config file
// down the structural-diff arm and show the user a diff of a tree that is not
// how they think about the file.
func Parse(data []byte) (any, error) {
	if bytes.HasPrefix(data, binaryMagic) {
		return parseBinary(data)
	}
	return parseXML(data)
}

// notAPlist wraps a reason as an ErrNotAPlist, so that a caller can tell the
// classification outcome from a read error while a message still says what was
// wrong with the bytes.
func notAPlist(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrNotAPlist, fmt.Sprintf(format, args...))
}
