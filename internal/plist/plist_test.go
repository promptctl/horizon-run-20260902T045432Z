package plist

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The fixtures under testdata are two spellings of ONE property list, produced
// by macOS itself -- written as XML by hand, converted with `plutil -convert
// binary1`, and both accepted by `plutil -lint` -- and committed rather than
// built at test time. That is the point of them: this package reads a format
// it does not write, so a fixture it generated would only prove it agrees with
// itself, and a fixture built at test time would need plutil on the machine
// running the suite, which a Linux CI container does not have.
//
// The pair is chosen to hold one of everything the value model has, plus the
// three encodings that only the binary format uses: an eight-byte signed
// integer (the -2), an ASCII string whose length escapes into a following
// integer object (the forty-character one), and a UTF-16 string carrying a
// surrogate pair (the cat).
//
//	settings.plist         the XML spelling
//	settings.binary.plist  the same document, converted by plutil
//	keyed.binary.plist     a UID object, which this reader refuses; plutil
//	                       writes one for the CF$UID dictionary NSKeyedArchiver
//	                       uses, and `plutil -p` shows it as a
//	                       CFKeyedArchiverUID
const (
	xmlFixture    = "testdata/settings.plist"
	binaryFixture = "testdata/settings.binary.plist"
	keyedFixture  = "testdata/keyed.binary.plist"
)

func read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}

func parse(t *testing.T, path string) any {
	t.Helper()
	value, err := Parse(read(t, path))
	if err != nil {
		t.Fatalf("Parse(%s): %v", path, err)
	}
	return value
}

// The property this whole package exists for. appspec/06 says two plists are
// "compared by parsed content", and the two fixtures are the same content in
// the two encodings macOS actually writes: a reader that got the epoch, the
// signedness, the UTF-16 decoding or the count escape wrong would still parse
// both files and would disagree about them here.
func TestTheXMLAndBinarySpellingsOfOneDocumentRenderIdentically(t *testing.T) {
	fromXML := Format(parse(t, xmlFixture))
	fromBinary := Format(parse(t, binaryFixture))
	if !reflect.DeepEqual(fromXML, fromBinary) {
		t.Errorf("the two spellings of one document render differently:\nXML:\n%s\nbinary:\n%s",
			strings.Join(fromXML, "\n"), strings.Join(fromBinary, "\n"))
	}
}

// Each type read back individually, from BOTH spellings, so that a failure
// names the type rather than pointing at a whole rendering. The cross-format
// case above would catch a reader that was wrong the same way twice; this one
// catches a reader that is wrong once.
func TestEveryPropertyListTypeIsReadBackFromBothSpellings(t *testing.T) {
	for _, fixture := range []string{xmlFixture, binaryFixture} {
		document, isDict := parse(t, fixture).(map[string]any)
		if !isDict {
			t.Fatalf("%s: the document is not a dictionary", fixture)
		}
		for _, want := range []struct {
			key   string
			value any
		}{
			{"name", "Ada"},
			{"count", int64(7)},
			// Signed, and eight bytes wide in the binary spelling:
			// CoreFoundation writes every negative integer that way, so a
			// reader treating the widest form as unsigned reports this as
			// 18446744073709551614.
			{"negative", int64(-2)},
			{"wide", int64(4294967296)},
			{"ratio", 1.5},
			// A real that is a whole number, kept apart from the integer 7
			// above by the value model and, separately, by the rendering.
			{"whole", 1.0},
			{"on", true},
			{"off", false},
			// The epoch is CoreFoundation's, not Unix's: a reader using
			// 1970 puts this date thirty-one years away.
			{"when", time.Date(2001, time.January, 2, 3, 4, 5, 0, time.UTC)},
			{"blob", []byte("hello")},
			// A surrogate pair (the cat) and characters outside ASCII: the
			// binary spelling stores this as UTF-16 code units, so the count
			// is not the number of characters and not the number of bytes.
			{"unicode", "café — 日本語 🐈"},
			{"empty dict", map[string]any{}},
			{"empty array", []any{}},
			{"long", "0123456789012345678901234567890123456789"},
		} {
			got, present := document[want.key]
			if !present {
				t.Errorf("%s: no %q key", fixture, want.key)
				continue
			}
			if !reflect.DeepEqual(got, want.value) {
				t.Errorf("%s: %q = %#v, want %#v", fixture, want.key, got, want.value)
			}
		}

		list, isArray := document["list"].([]any)
		if !isArray || len(list) != 3 {
			t.Fatalf("%s: list = %#v, want a three-element array", fixture, document["list"])
		}
		nested, isDict := list[2].(map[string]any)
		if !isDict {
			t.Fatalf("%s: list[2] = %#v, want a dictionary", fixture, list[2])
		}
		if !reflect.DeepEqual(nested["deep"], []any{0.25}) {
			t.Errorf("%s: list[2][deep] = %#v, want [0.25]", fixture, nested["deep"])
		}
	}
}

// The contract Format's doc states and the comparison in internal/drift relies
// on: equal renderings mean equal content. Every pair below is two DIFFERENT
// values that a careless rendering would print the same way, and the case
// fails if any two of them collide.
func TestTheRenderingTellsEveryValueApart(t *testing.T) {
	seen := map[string]any{}
	for _, value := range []any{
		int64(1),
		1.0,
		"1",
		true,
		[]byte{0x01},
		int64(0),
		0.0,
		"",
		false,
		[]byte{},
		map[string]any{},
		[]any{},
		map[string]any{"a": int64(1)},
		[]any{int64(1)},
		time.Date(2001, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2001, time.January, 1, 0, 0, 0, 1, time.UTC),
		"2001-01-01T00:00:00Z",
		// Two keys whose obvious concatenation collides: a rendering that did
		// not quote keys would print both dictionaries as the same two lines.
		map[string]any{"a: 1": int64(2)},
		map[string]any{"a": int64(1), "1": int64(2)},
	} {
		rendering := strings.Join(Format(value), "\n")
		if other, collided := seen[rendering]; collided {
			t.Errorf("%#v and %#v both render as:\n%s", value, other, rendering)
		}
		seen[rendering] = value
	}
}

func TestDictionaryKeysAreRenderedInSortedOrder(t *testing.T) {
	// A property-list dictionary is unordered and a Go map keeps no order at
	// all, so without the sort this rendering is a different diff on every
	// run and two files holding the same settings compare as differing.
	//
	// Eight keys and not three, because a map with three has a one-in-six
	// chance of iterating in order and a case that passes one run in six is
	// not a case. Go randomises map iteration precisely so that this kind of
	// dependency cannot hide.
	got := Format(map[string]any{
		"zebra": int64(1), "alpha": int64(2), "middle": int64(3), "delta": int64(4),
		"omega": int64(5), "beta": int64(6), "kappa": int64(7), "sigma": int64(8),
	})
	want := []string{"{",
		`  "alpha": 2`, `  "beta": 6`, `  "delta": 4`, `  "kappa": 7`,
		`  "middle": 3`, `  "omega": 5`, `  "sigma": 8`, `  "zebra": 1`, "}"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Format = %#v, want %#v", got, want)
	}
}

func TestABinaryPropertyListHoldingAUIDIsRefusedRatherThanModelled(t *testing.T) {
	// The package doc's reason: a UID has no XML spelling, so no cross-format
	// pair can agree about one, and appspec/06's byte-for-byte arm is the
	// honest answer for a keyed archive. What matters is that it is an error
	// rather than a value invented for it, which would compare equal to
	// something it is not.
	if value, err := Parse(read(t, keyedFixture)); err == nil {
		t.Errorf("a UID parsed as %#v; it must be refused so the caller falls through to a byte comparison", value)
	} else if !errors.Is(err, ErrNotAPlist) {
		t.Errorf("Parse: %v, want an ErrNotAPlist", err)
	}
}

func TestFilesThatAreNotPropertyListsAreRefused(t *testing.T) {
	// The classification appspec/06 asks for. Everything here is a file this
	// program plausibly syncs, and treating any of them as a plist would send
	// a file the user thinks of as text down the structural-diff arm.
	for name, content := range map[string]string{
		"an empty file":      "",
		"a text config file": "--ignore-dir=.git\n--type-add=perl:ext:pl\n",
		"an ini file":        "[application]\nname = Ack\n",
		"well-formed XML":    "<?xml version=\"1.0\"?>\n<settings><name>Ada</name></settings>\n",
		// The entry that observes the <plist> requirement, and the reason it is
		// written out beside the one above rather than left to it. That one is
		// refused whether or not the document element is checked, because
		// <name> is not a property-list element either; this one's single child
		// IS a property-list element, so without the check it parses as the
		// string "hello" and an ordinary XML config file starts being compared
		// as a structure. Injection found the gap: deleting the check left
		// every other entry here green.
		"XML whose only child looks like a plist value": "<config><string>hello</string></config>",
		"a plist element with no value":                 "<plist version=\"1.0\"></plist>",
		"a plist element with two values":               "<plist version=\"1.0\"><string>a</string><string>b</string></plist>",
		"a dict whose key has no value":                 "<plist version=\"1.0\"><dict><key>a</key></dict></plist>",
		"a dict with a value where a key belongs":       "<plist version=\"1.0\"><dict><string>a</string></dict></plist>",
		"an element that is not a plist type":           "<plist version=\"1.0\"><number>1</number></plist>",
		"an integer that is not a number":               "<plist version=\"1.0\"><integer>seven</integer></plist>",
		"a date that is not a date":                     "<plist version=\"1.0\"><date>yesterday</date></plist>",
		"data that is not base64":                       "<plist version=\"1.0\"><data>!!!</data></plist>",
		"the binary magic and nothing else":             "bplist00",
	} {
		if value, err := Parse([]byte(content)); err == nil {
			t.Errorf("%s parsed as %#v, so it would be compared as a property list", name, value)
		} else if !errors.Is(err, ErrNotAPlist) {
			t.Errorf("%s: Parse: %v, want an ErrNotAPlist", name, err)
		}
	}
}

func TestBase64DataIsReadAcrossTheLinesCoreFoundationWrapsItOn(t *testing.T) {
	// CoreFoundation wraps a long <data> payload across indented lines, and
	// encoding/base64 rejects the newlines rather than ignoring them. A reader
	// that did not strip them would refuse every plist holding an icon or a
	// window frame -- which is most of the preference files this program syncs.
	value, err := Parse([]byte("<plist version=\"1.0\"><data>\n\taGVs\n\tbG8=\n\t</data></plist>"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !reflect.DeepEqual(value, []byte("hello")) {
		t.Errorf("Parse = %#v, want the five bytes of \"hello\"", value)
	}
}

func TestWhitespaceInsideAStringIsKept(t *testing.T) {
	// Trimming a <string> is the tempting symmetry with <integer> and <date>,
	// which are trimmed here. It would silently rewrite a config value that IS
	// whitespace -- an indented block, a separator of one space -- and this
	// package's answer would then be that two files agree when the program is
	// about to replace one with the other.
	value, err := Parse([]byte("<plist version=\"1.0\"><string>  spaced  </string></plist>"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if value != "  spaced  " {
		t.Errorf("Parse = %q, want the leading and trailing spaces kept", value)
	}
}

func TestATruncatedBinaryPropertyListIsRefusedRatherThanCrashing(t *testing.T) {
	// A preference file interrupted mid-write is an ordinary thing to meet on
	// the path this package sits on, and appspec/06's answer for a file it
	// cannot parse is the byte-for-byte arm -- an error the caller falls
	// through on, never a panic that ends a sync run partway.
	whole := read(t, binaryFixture)
	for length := 0; length < len(whole); length++ {
		if _, err := Parse(whole[:length]); err == nil {
			t.Errorf("the first %d bytes of a %d-byte binary plist parsed as a whole document", length, len(whole))
		}
	}
}

func TestACorruptedBinaryPropertyListIsRefusedRatherThanCrashing(t *testing.T) {
	// Truncation only ever shortens the file. This corrupts the fields that
	// are USED as lengths and offsets -- the trailer's widths and counts, the
	// offset table, every object marker -- which is where a reader without
	// bounds checks indexes outside the slice.
	whole := read(t, binaryFixture)
	for at := range whole {
		for _, value := range []byte{0x00, 0x01, 0x0F, 0x7F, 0xFF} {
			corrupt := append([]byte(nil), whole...)
			corrupt[at] = value
			// The result is not asserted: a single-byte change can leave a
			// document that is still valid and says something else. What is
			// asserted is that Parse returns at all.
			_, _ = Parse(corrupt)
		}
	}
}

func TestABinaryPropertyListThatContainsItselfIsRefused(t *testing.T) {
	// The format stores containers as references into one flat table, so
	// nothing in it prevents an object from referring to itself. Without the
	// guard this is not a wrong answer, it is a sync run that never returns.
	//
	// Built byte by byte because no writer produces one: an eight-byte header,
	// a one-element array at offset 8 whose element is object #0 -- itself --
	// a one-entry offset table, and the thirty-two-byte trailer that declares
	// one-byte offsets, one-byte references, one object, top object #0, and
	// the table at offset 10.
	cyclic := append([]byte("bplist00"), 0xA1, 0x00, 0x08)
	trailer := make([]byte, trailerSize)
	trailer[offsetSizeAt] = 1
	trailer[refSizeAt] = 1
	trailer[objectCountAt+7] = 1
	trailer[offsetTableAt+7] = 10
	cyclic = append(cyclic, trailer...)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if value, err := Parse(cyclic); err == nil {
			t.Errorf("a self-referential array parsed as %#v", value)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		// A failure rather than a hang: an unguarded reader recurses until it
		// runs out of stack, and reporting that as a timeout says which case
		// found it.
		t.Fatal("Parse did not return on a self-referential array")
	}
}

func TestADeeplyNestedPropertyListIsRefusedRatherThanCrashing(t *testing.T) {
	// Both readers recurse once per level, and a stack exhausted in Go is a
	// fatal error rather than a returned one: it would take a whole sync run
	// down instead of falling through to appspec/06's byte-for-byte arm. The
	// depth here is far past anything CoreFoundation writes and far short of
	// what it takes to crash an unguarded reader, so what the case pins is
	// that the refusal happens at all.
	//
	// Both spellings, because the two readers count depth differently -- the
	// XML one carries it down the recursion and the binary one takes it from
	// the set of containers it has open.
	// The boundary from both sides, because a guard checked only from the
	// wrong side is a guard that is off by one and says nothing: a document
	// nested exactly maxDepth deep must still parse.
	for _, depth := range []int{maxDepth, maxDepth + 1} {
		xml := "<plist version=\"1.0\">" + strings.Repeat("<array>", depth) +
			strings.Repeat("</array>", depth) + "</plist>"
		_, err := Parse([]byte(xml))
		if (err == nil) != (depth <= maxDepth) {
			t.Errorf("an XML property list %d deep: err = %v, want refused only past %d", depth, err, maxDepth)
		}
		_, err = Parse(nestedBinary(depth))
		if (err == nil) != (depth <= maxDepth) {
			t.Errorf("a binary property list %d deep: err = %v, want refused only past %d", depth, err, maxDepth)
		}
	}
}

// nestedBinary builds a binary property list of exactly depth nested arrays:
// one array object per level holding a reference to the next, and an empty one
// innermost.
//
// Exactly depth, which took a correction: the first spelling wrote depth
// arrays and then an empty one INSIDE the last, so it built depth+1 containers
// and reported the binary reader as one level stricter than the XML reader
// when the two agree.
//
// Built by hand because nothing writes a file like this, which is the point --
// the guard is for a file that is not what it claims to be. Offsets and
// references are two bytes wide, since the objects run past the 256 a
// single-byte width could address.
func nestedBinary(depth int) []byte {
	var objects []byte
	offsets := make([]int, depth)
	for i := 0; i < depth; i++ {
		offsets[i] = len(binaryMagic) + len(objects)
		if i == depth-1 {
			objects = append(objects, 0xA0)
			break
		}
		objects = append(objects, 0xA1, byte((i+1)>>8), byte(i+1))
	}
	file := append([]byte(nil), binaryMagic...)
	file = append(file, objects...)
	table := len(file)
	for _, offset := range offsets {
		file = append(file, byte(offset>>8), byte(offset))
	}
	trailer := make([]byte, trailerSize)
	trailer[offsetSizeAt] = 2
	trailer[refSizeAt] = 2
	trailer[objectCountAt+7], trailer[objectCountAt+6] = byte(len(offsets)), byte(len(offsets)>>8)
	trailer[offsetTableAt+7], trailer[offsetTableAt+6] = byte(table), byte(table>>8)
	return append(file, trailer...)
}
