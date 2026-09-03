package plist

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"reflect"
	"runtime"
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

func TestATrailerThatWrapsTheOffsetTableIsRefusedRatherThanCrashing(t *testing.T) {
	// The offset table's start is eight bytes taken verbatim from the trailer,
	// so a corrupt file can set it to the top of the uint64 range. Added to
	// the table's length that wraps to a small number, which passes a
	// does-it-fit check written as an addition and then slices the file from
	// past its end to before it -- a panic, out of Parse and through Compare,
	// which is the one outcome appspec/06 has no arm for.
	//
	// Built by hand rather than by corrupting the fixture, because
	// single-byte corruption cannot reach it: the wrap needs the whole
	// eight-byte field set, so the case above would report this as covered
	// while never producing it.
	wrapping := append([]byte("bplist00"), 0xA0)
	trailer := make([]byte, trailerSize)
	trailer[offsetSizeAt] = 1
	trailer[refSizeAt] = 1
	trailer[objectCountAt+7] = 1
	for at := 0; at < 8; at++ {
		trailer[offsetTableAt+at] = 0xFF
	}
	if value, err := Parse(append(wrapping, trailer...)); err == nil {
		t.Errorf("an offset table at the top of the range parsed as %#v", value)
	} else if !errors.Is(err, ErrNotAPlist) {
		t.Errorf("Parse: %v, want an ErrNotAPlist", err)
	}
}

// allocatedParsing reports the megabytes Parse allocates on data, with the
// value it returned.
//
// Measured rather than inferred, because the two cases below are about
// allocation and one of them returns an error either way: a nested chain of
// oversized containers is refused for its DEPTH whether or not the counts it
// declares are charged for, so a case that asserted only the error would have
// been green while the reader allocated a quarter of a gigabyte on the way to
// producing it.
func allocatedParsing(t *testing.T, data []byte) (uint64, error) {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := Parse(data)
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) >> 20, err
}

// binaryTrailer builds the thirty-two-byte trailer for a file with width-byte
// offsets and references, count objects, the top object #0, and its offset
// table at table.
//
// The counts go in through PutUint64 rather than by poking the low bytes,
// which is how the cases below were first written and how one of them was
// silently wrong: a fixture whose offset table sat past 65535 had its position
// truncated into two bytes, so the file was malformed for an unrelated reason
// and the case passed while observing nothing. Injection is what surfaced it.
func binaryTrailer(width, count, table int) []byte {
	trailer := make([]byte, trailerSize)
	trailer[offsetSizeAt] = byte(width)
	trailer[refSizeAt] = byte(width)
	binary.BigEndian.PutUint64(trailer[objectCountAt:], uint64(count))
	binary.BigEndian.PutUint64(trailer[offsetTableAt:], uint64(table))
	return trailer
}

func TestContainersThatDeclareMoreThanTheyHoldAreRefusedBeforeTheyAllocate(t *testing.T) {
	// A count is a number the file declares, and the only thing it has to fit
	// is the objects region -- which every container can see to the end of,
	// wherever it sits. So each level here declares twenty thousand elements
	// in a forty-five-kilobyte file and is within its rights; each allocates
	// for them before recursing, and none of it unwinds until the depth guard
	// fires five hundred levels down. Unbudgeted, that is 239 MiB from 45 KiB,
	// and it scales with the file: a megabyte of the same layout is gigabytes,
	// which is a sync run killed rather than a comparison declined.
	const levels = 512
	const declared = 20000
	var objects []byte
	offsets := make([]int, levels)
	for i := 0; i < levels; i++ {
		offsets[i] = len(binaryMagic) + len(objects)
		if i == levels-1 {
			objects = append(objects, 0xA0)
			break
		}
		// 0xAF is an array whose count escapes to the integer object that
		// follows, and 0x12 is that integer, four bytes wide.
		objects = append(objects, 0xAF, 0x12,
			byte(declared>>24), byte(declared>>16), byte(declared>>8), byte(declared&0xFF),
			byte((i+1)>>8), byte(i+1))
	}
	// Room enough that the declared references genuinely fit the region, so
	// the case is refused by the budget rather than by the check above it.
	objects = append(objects, make([]byte, declared*2+16)...)

	greedy := append([]byte(nil), binaryMagic...)
	greedy = append(greedy, objects...)
	table := len(greedy)
	for _, offset := range offsets {
		greedy = append(greedy, byte(offset>>8), byte(offset))
	}
	greedy = append(greedy, binaryTrailer(2, len(offsets), table)...)

	allocated, err := allocatedParsing(t, greedy)
	if err == nil {
		t.Error("a chain of containers each declaring twenty thousand elements parsed")
	} else if !errors.Is(err, ErrNotAPlist) {
		t.Errorf("Parse: %v, want an ErrNotAPlist", err)
	}
	// Generously above what the budget permits and an order of magnitude below
	// what the defect produced, so the case says which of the two happened
	// without being sensitive to how Go sizes a slice.
	if allocated > maxBytes>>20+32 {
		t.Errorf("Parse allocated %d MiB on a %d-byte file", allocated, len(greedy))
	}
}

func TestAScalarReferencedManyTimesIsRefusedRatherThanCopiedEachTime(t *testing.T) {
	// The same defect without the nesting, and the one that does not even
	// announce itself: one array of twenty thousand references, every one
	// naming the same twenty-thousand-unit scalar, which is materialised
	// afresh at each reference because nothing here memoises. Unbudgeted this
	// PARSES -- 60 KiB in, 391 MiB out -- and Format then renders twenty
	// thousand lines of twenty thousand characters. It is quadratic in the
	// file's length, so a megabyte of it is a hundred gigabytes.
	//
	// All three counted scalar kinds, because they are three separate arms
	// with three separate allocations and a charge belongs in each. Injection
	// is how that was learned: the case was first written for the ASCII arm
	// alone, and deleting the charge from either of the other two left it
	// green.
	const references = 20000
	const length = 20000
	for name, scalar := range map[string]struct {
		marker byte
		bytes  int
	}{
		"data":   {0x4F, length},
		"ASCII":  {0x5F, length},
		"UTF-16": {0x6F, length * 2},
	} {
		// 0xAF and the scalar markers escape their count into the four-byte
		// integer object 0x12 that follows.
		array := []byte{0xAF, 0x12, 0, 0, byte(references >> 8), byte(references & 0xFF)}
		for i := 0; i < references; i++ {
			array = append(array, 0, 1)
		}
		content := []byte{scalar.marker, 0x12, 0, 0, byte(length >> 8), byte(length & 0xFF)}
		content = append(content, make([]byte, scalar.bytes)...)

		shared := append([]byte(nil), binaryMagic...)
		first := len(shared)
		shared = append(shared, array...)
		second := len(shared)
		shared = append(shared, content...)
		table := len(shared)
		for _, offset := range []int{first, second} {
			shared = append(shared, byte(offset>>8), byte(offset))
		}
		shared = append(shared, binaryTrailer(2, 2, table)...)

		allocated, err := allocatedParsing(t, shared)
		if err == nil {
			t.Errorf("twenty thousand references to one %s scalar parsed", name)
		} else if !errors.Is(err, ErrNotAPlist) {
			t.Errorf("%s: Parse: %v, want an ErrNotAPlist", name, err)
		}
		// Generously above what the budget permits and far below what the
		// defect produced, so the case says which of the two happened without
		// being sensitive to how Go sizes an allocation.
		if allocated > maxBytes>>20+32 {
			t.Errorf("%s: Parse allocated %d MiB on a %d-byte file", name, allocated, len(shared))
		}
	}
}

func TestAChargeTooLargeToPayIsRefusedRatherThanWrapped(t *testing.T) {
	// spend divides rather than multiplying, and this is the only thing that
	// can see it: a charge whose product overflows int wraps to a small number
	// and PASSES a check written the obvious way, which is the same defect the
	// budget exists to prevent, one level down.
	//
	// Called directly because Parse cannot reach it on this host -- count is
	// bounded by the file's length, so overflowing a 64-bit int would take an
	// exabyte -- while on a 32-bit host, where int is 32 bits, a 64 MiB
	// property list reaches it and the suite does not run there. Testing the
	// helper's contract is what is available; the alternative is a guard
	// nothing observes.
	reader := &binaryReader{budget: maxBytes}
	if err := reader.spend(math.MaxInt/4, referenceCost); err == nil {
		t.Error("a charge whose product overflows int was paid out of a 64 MiB budget")
	}
	if reader.budget != maxBytes {
		t.Errorf("budget = %d after a refused charge, want %d", reader.budget, maxBytes)
	}
}

func TestABinaryPropertyListThatSharesOneSubtreeManyTimesIsRefused(t *testing.T) {
	// The other shape of the same failure as the cycle below, and the one the
	// cycle guard cannot see: sharing rather than recursion. Every object here
	// is reachable only from above, so nothing contains itself, and forty
	// levels is well inside maxDepth -- but each array names the next one
	// TWICE, so the tree it expands to doubles per level. 198 bytes, 2^39
	// values, and a reader bounded only by depth and cycles never returns.
	//
	// Refusal, not a slow success: the budget is what makes it terminate, and
	// a document that large has no readable diff anyway, so appspec/06's
	// byte-for-byte arm is where it belongs.
	const levels = 40
	var objects []byte
	offsets := make([]int, levels)
	for i := 0; i < levels; i++ {
		offsets[i] = len(binaryMagic) + len(objects)
		if i == levels-1 {
			objects = append(objects, 0xA0)
			break
		}
		objects = append(objects, 0xA2, byte(i+1), byte(i+1))
	}
	sharing := append([]byte(nil), binaryMagic...)
	sharing = append(sharing, objects...)
	table := len(sharing)
	for _, offset := range offsets {
		sharing = append(sharing, byte(offset))
	}
	trailer := make([]byte, trailerSize)
	trailer[offsetSizeAt] = 1
	trailer[refSizeAt] = 1
	trailer[objectCountAt+7] = byte(levels)
	trailer[offsetTableAt+7] = byte(table)
	sharing = append(sharing, trailer...)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if value, err := Parse(sharing); err == nil {
			t.Errorf("a doubling reference graph parsed as %#v", value)
		} else if !errors.Is(err, ErrNotAPlist) {
			t.Errorf("Parse: %v, want an ErrNotAPlist", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Parse did not return on a doubling reference graph")
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
