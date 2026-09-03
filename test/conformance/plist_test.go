//go:build conformance

// The property-list arm of appspec/06 "Drift detection", from outside.
//
// appspec/06 gives two regular files three comparisons in a fixed order, and
// the first is "if both parse as property-list (plist) files: compared by
// parsed content". sync_test.go pins that the arm EXISTS -- two XML spellings
// of one dictionary are identical, and two that differ produce a diff of their
// structures rather than of their markup. This file is about what the reader
// underneath it has to get right for that arm to mean anything, and about
// where the arm has to decline.
//
// Two observables, and every case below is one of them.
//
// A pair that BOTH parse takes the plist arm, so a reader that misreads one
// spelling of a value reports two files holding the same settings as
// differing: the run stops at a prompt it should never have reached. Those
// cases run with stdin at end-of-input, which appspec/07 makes a non-zero
// exit, so "identical" is checked rather than asserted.
//
// A file the reader REFUSES falls out of the arm, and the pair is compared as
// text or as bytes instead. So a refusal is visible as the output of the arm
// below it -- "binary contents differ", or a diff of the markup itself. That
// is what makes the reader's refusals observable out here at all, and it is
// why each of those cases asserts the OTHER arm's output.

package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plistFixture reads one of internal/plist's own testdata files.
//
// Borrowed rather than written here, for the reason config_test.go borrows
// internal/storage's SQLite fixtures: this program cannot produce a binary
// property list, and a hand-rolled one would be this suite agreeing with the
// reader it is supposed to be checking from outside. settings.plist and
// settings.binary.plist are one document in both spellings, converted by
// plutil -- so the pair is macOS's own answer to "are these the same file",
// which is exactly the question the arm has to get right.
func plistFixture(t *testing.T, name string) string {
	t.Helper()
	root, err := moduleRoot()
	if err != nil {
		t.Fatalf("locating the module root: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "internal/plist/testdata", name))
	if err != nil {
		t.Fatalf("reading the fixture %s: %v", name, err)
	}
	return string(content)
}

// dictPlist wraps a body of <key>/<value> pairs in the XML envelope macOS
// writes, so a case shows the values it is about and nothing else.
func dictPlist(body string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
` + body + `</dict>
</plist>
`
}

func TestTheTwoSpellingsOfOneDocumentAgreeOnEveryTypeItHolds(t *testing.T) {
	// One case, four defects. internal/plist's testdata holds one document in
	// both spellings, and that document was built to carry the values whose
	// binary encoding is easy to get wrong: a date (stored as seconds from
	// 2001, not from 1970), a negative integer (an eight-byte field whose sign
	// bit a reader can mask off), a string with an emoji in it (a surrogate
	// PAIR in the UTF-16 the binary format uses, which decodes to one rune and
	// widens to two), and fifteen dictionary keys (one more than the four-bit
	// count field holds, so the binary spelling takes the escape that puts the
	// count in a following integer).
	//
	// Any one of those read wrongly makes the two spellings render differently,
	// so the pair is reported as drift: the user is asked whether to replace a
	// file that holds exactly the settings the other one does. That is the
	// failure this case is here for, and it is invisible to a fixture holding
	// only strings.
	//
	// Run with stdin at end-of-input, so "identical" is checked and not
	// asserted: appspec/07 makes a prompt reached with no answer available a
	// non-zero exit, so a program that found any difference fails here rather
	// than being quietly answered.
	world := newSyncWorld(t, ".probplist")
	world.WriteFile(".probplist", plistFixture(t, "settings.plist"), 0o600)
	world.WriteMackup(".probplist", plistFixture(t, "settings.binary.plist"), 0o600)

	before := world.Snapshot()
	result := world.Run("backup", probeKey).ExpectExit(0).ExpectSilentStderr()
	if result.StdoutText() != "" {
		t.Errorf("backup printed %q, want nothing: the two files are one document in two spellings", result.Stdout)
	}
	world.ExpectUnchanged(before)
}

func TestABinaryPropertyListHoldingAUIDIsNotComparedAsAPropertyList(t *testing.T) {
	// The reader models the types appspec/06's comparison needs and refuses
	// everything else rather than guessing. A UID is the marker plutil emits
	// for a keyed archive, and there is no value in this program's model that
	// means one -- so a reader that accepted it would have to invent a
	// rendering, and two archives whose UIDs differ would compare by whatever
	// that invention happened to be.
	//
	// A refusal is not silence: the pair simply falls out of the plist arm and
	// is compared by the arm below, which for two binary files is the
	// one-line note. So the assertion is that note, plus the absence of a
	// hunk header -- a program that had parsed the UID would print a diff of
	// two structures here.
	world := newSyncWorld(t, ".probplist")
	world.WriteFile(".probplist", plistFixture(t, "keyed.binary.plist"), 0o600)
	world.WriteMackup(".probplist", plistFixture(t, "settings.binary.plist"), 0o600)

	stdout := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0).StdoutText()

	if want := "binary contents differ"; !strings.Contains(stdout, want) {
		t.Errorf("backup stdout = %q, want %q: a file the reader refuses is compared as bytes", stdout, want)
	}
	if strings.Contains(stdout, "@@ -") {
		t.Errorf("backup stdout = %q, want no diff: neither file took the property-list arm", stdout)
	}
}

func TestAnXMLDocumentWhoseRootIsNotPlistIsNotAPropertyList(t *testing.T) {
	// A well-formed XML document holding a <string> is not a property list,
	// and the check that says so is the document element. It is the easiest
	// check in the reader to drop, because every other refusal in it fires on
	// something malformed and this one fires on a file that parses perfectly.
	//
	// Observable through the arm below: both files are UTF-8 text, so the pair
	// is diffed AS TEXT and the markup itself appears in the output -- which
	// is exactly what TestAPropertyListDiffShowsTheStructureAndNotTheMarkup
	// asserts must NOT happen for a real property list. The two cases are the
	// same claim from opposite sides.
	world := newSyncWorld(t, ".probplist")
	world.WriteFile(".probplist", "<notplist><string>home</string></notplist>\n", 0o600)
	world.WriteMackup(".probplist", dictPlist("\t<key>k</key>\n\t<string>storage</string>\n"), 0o600)

	stdout := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0).StdoutText()

	if want := "<notplist>"; !strings.Contains(stdout, want) {
		t.Errorf("backup stdout = %q, want the markup %q in a TEXT diff: neither side is a property list", stdout, want)
	}
}

func TestWhitespaceInsideAPropertyListStringIsKept(t *testing.T) {
	// The XML reader trims the text of the numeric elements, because
	// CoreFoundation writes `<integer> 7 </integer>` and the whitespace is not
	// part of the number. A <string> is the opposite: its whitespace IS the
	// value, and trimming it makes two settings that differ compare as one.
	//
	// The two files differ ONLY in that padding, so a reader that trimmed
	// would report them identical, print nothing and skip -- and this case
	// would fail on the missing diff rather than on a wrong one.
	world := newSyncWorld(t, ".probplist")
	world.WriteFile(".probplist", dictPlist("\t<key>k</key>\n\t<string>  padded  </string>\n"), 0o600)
	world.WriteMackup(".probplist", dictPlist("\t<key>k</key>\n\t<string>padded</string>\n"), 0o600)

	stdout := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0).StdoutText()

	for _, want := range []string{`+  "k": "  padded  "`, `-  "k": "padded"`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("backup stdout = %q, want the diff line %q: the padding is part of the string", stdout, want)
		}
	}
}

func TestBase64DataIsReadAcrossTheLinesCoreFoundationWrapsItOn(t *testing.T) {
	// CoreFoundation writes <data> wrapped and indented, and base64 decoders
	// reject the newlines and tabs that wrapping puts in. So the reader strips
	// them, and the two files here hold the SAME bytes written both ways.
	//
	// A reader that did not strip would fail to decode the wrapped one, refuse
	// the file, and drop the pair to the text arm -- where two spellings of one
	// blob are plainly different text, so the run reaches a prompt. Run with
	// stdin at end-of-input, which turns that prompt into a non-zero exit.
	wrapped := "\t<key>blob</key>\n\t<data>\n" +
		"\t\tbWFja2xlYm94IHJlYWRzIGJh\n" +
		"\t\tc2U2NCBkYXRhIHRoZSB3YXkg\n" +
		"\t\tQ29yZUZvdW5kYXRpb24gd3Jp\n" +
		"\t\tdGVzIGl0OiB3cmFwcGVkLg==\n" +
		"\t</data>\n"
	oneLine := "\t<key>blob</key>\n\t<data>bWFja2xlYm94IHJlYWRzIGJhc2U2NCBkYXRhIHRoZSB3YXkg" +
		"Q29yZUZvdW5kYXRpb24gd3JpdGVzIGl0OiB3cmFwcGVkLg==</data>\n"

	world := newSyncWorld(t, ".probplist")
	world.WriteFile(".probplist", dictPlist(wrapped), 0o600)
	world.WriteMackup(".probplist", dictPlist(oneLine), 0o600)

	before := world.Snapshot()
	result := world.Run("backup", probeKey).ExpectExit(0).ExpectSilentStderr()
	if result.StdoutText() != "" {
		t.Errorf("backup printed %q, want nothing: the two files hold the same blob", result.Stdout)
	}
	world.ExpectUnchanged(before)
}

func TestAWholeRealIsNotTheIntegerBesideIt(t *testing.T) {
	// A property list may hold 1 and 1.0 in the same slot as different values,
	// and the rendering the diff is taken over has to keep them apart --
	// otherwise two files whose settings genuinely differ compare as one and
	// the destination is silently left stale.
	//
	// Go's shortest round-trip formatting prints a whole float as bare digits,
	// which is the integer spelling exactly, so the ".0" is what carries the
	// type. Dropping it is a one-character change that looks like tidying up.
	world := newSyncWorld(t, ".probplist")
	world.WriteFile(".probplist", dictPlist("\t<key>n</key>\n\t<real>1.0</real>\n"), 0o600)
	world.WriteMackup(".probplist", dictPlist("\t<key>n</key>\n\t<integer>1</integer>\n"), 0o600)

	stdout := world.RunWithInput("no\n", "backup", probeKey).ExpectExit(0).StdoutText()

	for _, want := range []string{`+  "n": 1.0`, `-  "n": 1`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("backup stdout = %q, want the diff line %q: a whole real is not the integer", stdout, want)
		}
	}
}
