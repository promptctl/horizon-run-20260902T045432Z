package plist

import (
	"encoding/binary"
	"math"
	"time"
	"unicode/utf16"
)

// The binary property list's fixed geometry, from the format itself: an
// eight-byte header, and a thirty-two-byte trailer at the very end of the file
// that says where everything else is.
const (
	trailerSize = 32
	// offsetSizeAt and the three that follow are byte positions inside the
	// trailer. The five bytes before offsetSizeAt are unused and the sixth is
	// a sort version this reader does not consult: it describes how the writer
	// ordered a set's members, and sets are refused here.
	offsetSizeAt  = 6
	refSizeAt     = 7
	objectCountAt = 8
	topObjectAt   = 16
	offsetTableAt = 24
)

// appleEpoch is the instant a binary plist date counts seconds from: midnight
// UTC on 1 January 2001, which is CoreFoundation's epoch and not Unix's. The
// two differ by 31 years, so a reader that used the wrong one would compare a
// date in an XML plist against a date thirty-one years away in the binary
// spelling of the same file and report drift on every run.
var appleEpoch = time.Date(2001, time.January, 1, 0, 0, 0, 0, time.UTC)

// parseBinary reads the binary spelling of a property list.
//
// The format is a flat table of objects plus an offset table that says where
// each one starts; containers hold indexes into that table rather than nested
// bytes. So this reads the trailer first to learn the table's geometry, then
// resolves the top object recursively.
//
// Every offset, index and length read out of the file is bounds-checked before
// it is used. That is not defensive habit: the bytes are a third party's, this
// program meets them on a path where a truncated or corrupt preference file is
// an ordinary thing to find, and appspec/06's answer for a file it cannot
// parse is the byte-for-byte arm -- an error -- rather than a panic that takes
// the whole run down mid-sync.
func parseBinary(data []byte) (any, error) {
	if len(data) < len(binaryMagic)+trailerSize {
		return nil, notAPlist("binary property list is shorter than its own header and trailer")
	}
	trailer := data[len(data)-trailerSize:]

	offsetSize := int(trailer[offsetSizeAt])
	refSize := int(trailer[refSizeAt])
	objectCount := binary.BigEndian.Uint64(trailer[objectCountAt:])
	topObject := binary.BigEndian.Uint64(trailer[topObjectAt:])
	offsetTable := binary.BigEndian.Uint64(trailer[offsetTableAt:])

	// The two widths are read from the file and multiply every table read
	// below, so they are checked before any of it. Eight is the largest the
	// format defines; zero would make the offset table empty and every object
	// start at the same place.
	if offsetSize < 1 || offsetSize > 8 || refSize < 1 || refSize > 8 {
		return nil, notAPlist("binary property list declares an offset width of %d and a reference width of %d", offsetSize, refSize)
	}
	// uint64 arithmetic throughout, and only then narrowed, so that a declared
	// count near the top of the range cannot overflow into a small in-range
	// int on a 64-bit host.
	if objectCount == 0 || objectCount > uint64(len(data)) {
		return nil, notAPlist("binary property list declares %d objects in %d bytes", objectCount, len(data))
	}
	// The table's start is bounded on its own, and from both sides, before it
	// is added to anything. offsetTable is eight bytes taken verbatim from the
	// trailer, so a corrupt file can set it to the top of the range, and
	// `offsetTable + length > room` would then wrap to a small number and pass
	// a check it fails -- a slice expression with a start past its end, which
	// panics rather than returning. The length is subtracted from the room
	// that is left instead, which cannot wrap: objects is at most
	// len(data)-trailerSize by the line above.
	objects := uint64(len(data) - trailerSize)
	if offsetTable < uint64(len(binaryMagic)) || offsetTable > objects {
		return nil, notAPlist("binary property list's offset table starts at %d, outside a %d-byte file", offsetTable, len(data))
	}
	if objectCount*uint64(offsetSize) > objects-offsetTable {
		return nil, notAPlist("binary property list's offset table does not fit in the file")
	}
	end := offsetTable + objectCount*uint64(offsetSize)
	if topObject >= objectCount {
		return nil, notAPlist("binary property list's top object is #%d of %d", topObject, objectCount)
	}

	reader := &binaryReader{
		data:        data,
		offsetTable: data[offsetTable:end],
		offsetSize:  offsetSize,
		refSize:     refSize,
		count:       int(objectCount),
		open:        map[int]bool{},
		budget:      maxValues,
	}
	return reader.object(int(topObject))
}

// maxValues bounds how many values one binary property list may expand to.
//
// The depth guard is not enough on its own, because the blow-up this format
// allows is not depth. The object table is flat and a reference may name any
// entry, so one object can be referenced from several places at once; that is
// not a cycle and it is not deep, and the tree it expands to is the PRODUCT of
// the sharing rather than the sum. Forty two-element arrays, each pointing
// twice at the next, are 198 bytes that expand to 2^39 values -- a sync run
// that never returns, waiting at a prompt.
//
// Bounding the count of values resolved bounds both halves of that: the work
// this reader does, and the size of the tree Format then walks to produce the
// diff. It has to be one bound rather than two, because a reader that returned
// the shared graph quickly would move the same hang into the renderer, which
// visits a shared subtree once per reference.
//
// Deliberately a constant rather than something scaled to the file's length.
// CoreFoundation uniques equal objects as it writes, so a file legitimately
// holding a thousand references to one shared dictionary expands to far more
// values than it has bytes, and a length-relative budget would refuse real
// preference files. This is three orders of magnitude above what one holds,
// and a document past it falls through to appspec/06's byte-for-byte arm --
// which is the honest answer for a structure no one would read a diff of.
const maxValues = 1 << 20

// A binaryReader resolves object references against one file's object table.
type binaryReader struct {
	data        []byte
	offsetTable []byte
	offsetSize  int
	refSize     int
	count       int
	// open holds the objects currently being resolved on this path, so that a
	// container referring to one of its own ancestors is reported rather than
	// followed. The format allows any reference, including a cycle, and the
	// recursion below would otherwise not terminate -- a hang inside a sync
	// run, which is worse than either of appspec/06's outcomes.
	open map[int]bool
	// budget is how many more values this document may expand to; see
	// maxValues. Spent per resolved object rather than per table entry,
	// because it is the expansion that is bounded, not the table.
	budget int
}

// offsetOf returns the byte offset of object ref within the file.
func (r *binaryReader) offsetOf(ref int) (int, error) {
	if ref < 0 || ref >= r.count {
		return 0, notAPlist("object reference #%d is outside the table of %d", ref, r.count)
	}
	offset := beUint(r.offsetTable[ref*r.offsetSize : (ref+1)*r.offsetSize])
	if offset >= uint64(len(r.data)-trailerSize) {
		return 0, notAPlist("object #%d starts at %d, past the end of the objects", ref, offset)
	}
	return int(offset), nil
}

// object resolves one object reference to its value.
//
// The marker byte's high nibble is the type and its low nibble is either a
// small count or the escape 0xF that says a count follows as an integer
// object. That shape is shared by the four sized kinds -- data, both string
// encodings, and the two containers -- so it is read once, by count.
func (r *binaryReader) object(ref int) (any, error) {
	if r.open[ref] {
		return nil, notAPlist("object #%d contains itself", ref)
	}
	// The open set holds exactly the containers on the path to here, so its
	// size is the nesting depth and no separate counter is needed.
	if len(r.open) >= maxDepth {
		return nil, notAPlist("property list nests more than %d deep", maxDepth)
	}
	if r.budget--; r.budget < 0 {
		return nil, notAPlist("property list expands to more than %d values", maxValues)
	}
	offset, err := r.offsetOf(ref)
	if err != nil {
		return nil, err
	}
	marker := r.data[offset]
	kind, low := marker>>4, int(marker&0x0F)

	switch kind {
	case 0x0:
		switch marker {
		case 0x08:
			return false, nil
		case 0x09:
			return true, nil
		}
		// 0x00 (null) and 0x0F (fill) are refused with the same argument the
		// package doc makes for UIDs and sets: neither has an XML spelling, so
		// neither can take part in a comparison across the two formats.
		return nil, notAPlist("object #%d has marker 0x%02x, which has no property-list value", ref, marker)
	case 0x1:
		return r.integer(offset, low)
	case 0x2:
		return r.real(offset, low)
	case 0x3:
		if marker != 0x33 {
			return nil, notAPlist("object #%d is a date of an unknown width", ref)
		}
		seconds, err := r.float(offset+1, 8)
		if err != nil {
			return nil, err
		}
		// Nanoseconds rather than a Duration multiplication, because the
		// stored value is a float64 of seconds and a Duration cannot hold the
		// range CoreFoundation writes. Rounding to the nanosecond is the same
		// resolution time.Time keeps.
		return appleEpoch.Add(time.Duration(math.Round(seconds * float64(time.Second)))), nil
	case 0x4, 0x5, 0x6, 0xA:
		count, body, err := r.sized(ref, offset, low)
		if err != nil {
			return nil, err
		}
		return r.sizedObject(ref, kind, count, body)
	case 0xD:
		count, body, err := r.sized(ref, offset, low)
		if err != nil {
			return nil, err
		}
		return r.dict(ref, count, body)
	}
	// 0x8 (UID) and 0xC (set) land here, and the package doc says why they are
	// refused rather than modelled.
	return nil, notAPlist("object #%d has marker 0x%02x, which this reader does not model", ref, marker)
}

// sized reads the element count of a counted object and returns the count with
// the bytes that follow it.
//
// The count is the marker's low nibble unless that nibble is 0xF, in which case
// an integer object follows the marker and holds the real count. That escape is
// how anything with more than fourteen elements is written, so it is the common
// case for a real preference file rather than an edge.
func (r *binaryReader) sized(ref, offset, low int) (int, []byte, error) {
	body := offset + 1
	count := low
	if low == 0x0F {
		if body >= len(r.data) {
			return 0, nil, notAPlist("object #%d ends before its count", ref)
		}
		marker := r.data[body]
		if marker>>4 != 0x1 {
			return 0, nil, notAPlist("object #%d has a count that is not an integer", ref)
		}
		width := 1 << (marker & 0x0F)
		size, err := r.integer(body, int(marker&0x0F))
		if err != nil {
			return 0, nil, err
		}
		if size < 0 {
			return 0, nil, notAPlist("object #%d declares a negative count", ref)
		}
		count = int(size)
		body += 1 + width
	}
	// Bounded against the file's own length before any arithmetic is done with
	// it. A count is a number the file declares, and the callers multiply it --
	// by two for a dictionary's key/value pairs, by the reference width, by two
	// again for UTF-16 code units -- so an unbounded one overflows int on the
	// way to the bounds check that was supposed to catch it, and a negative
	// product passes every "is it too big" test written the obvious way. No
	// object can have more elements than the file has bytes, so this is not a
	// heuristic limit.
	if count > len(r.data) {
		return 0, nil, notAPlist("object #%d declares %d elements in a %d-byte file", ref, count, len(r.data))
	}
	if body > len(r.data)-trailerSize {
		return 0, nil, notAPlist("object #%d ends before its contents", ref)
	}
	return count, r.data[body : len(r.data)-trailerSize], nil
}

// sizedObject builds the value of a counted object other than a dictionary:
// data, either string encoding, or an array.
func (r *binaryReader) sizedObject(ref int, kind byte, count int, body []byte) (any, error) {
	switch kind {
	case 0x4:
		if count > len(body) {
			return nil, notAPlist("object #%d declares %d bytes of data and has %d", ref, count, len(body))
		}
		// Copied, so the returned value does not alias the file's bytes and
		// cannot be changed under a caller holding it.
		return append([]byte(nil), body[:count]...), nil
	case 0x5:
		if count > len(body) {
			return nil, notAPlist("object #%d declares %d ASCII characters and has %d", ref, count, len(body))
		}
		return string(body[:count]), nil
	case 0x6:
		// The count is in UTF-16 code units, not bytes, and a character
		// outside the basic multilingual plane is two of them. Decoding
		// through utf16.Decode rather than widening each unit is what makes an
		// emoji or a CJK extension character survive the round trip into the
		// UTF-8 string model the XML side produces.
		if count > len(body)/2 {
			return nil, notAPlist("object #%d declares %d UTF-16 characters and has %d bytes", ref, count, len(body))
		}
		units := make([]uint16, count)
		for i := range units {
			units[i] = binary.BigEndian.Uint16(body[i*2:])
		}
		return string(utf16.Decode(units)), nil
	default:
		refs, err := r.references(ref, count, body)
		if err != nil {
			return nil, err
		}
		array := make([]any, 0, count)
		r.open[ref] = true
		defer delete(r.open, ref)
		for _, element := range refs {
			value, err := r.object(element)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		return array, nil
	}
}

// dict builds a dictionary, whose body is count key references followed by
// count value references.
func (r *binaryReader) dict(ref, count int, body []byte) (any, error) {
	pairs, err := r.references(ref, count*2, body)
	if err != nil {
		return nil, err
	}
	r.open[ref] = true
	defer delete(r.open, ref)

	dict := make(map[string]any, count)
	for i := 0; i < count; i++ {
		key, err := r.object(pairs[i])
		if err != nil {
			return nil, err
		}
		text, isText := key.(string)
		if !isText {
			// A non-string key cannot be rendered or compared against the XML
			// model, whose <key> is always text.
			return nil, notAPlist("object #%d has a key that is not a string", ref)
		}
		value, err := r.object(pairs[count+i])
		if err != nil {
			return nil, err
		}
		dict[text] = value
	}
	return dict, nil
}

// references reads count object references of the file's reference width.
func (r *binaryReader) references(ref, count int, body []byte) ([]int, error) {
	if count < 0 || count > len(body)/r.refSize {
		return nil, notAPlist("object #%d declares %d references and does not hold them", ref, count)
	}
	refs := make([]int, count)
	for i := range refs {
		refs[i] = int(beUint(body[i*r.refSize : (i+1)*r.refSize]))
	}
	return refs, nil
}

// integer reads the integer object whose marker is at offset, whose low nibble
// gives the width as a power of two.
//
// Widths of one, two and four bytes are unsigned and eight bytes is signed,
// which is CoreFoundation's own asymmetry rather than a choice here: it writes
// a negative number as a full eight-byte two's-complement value, which is why
// the -2 in the fixture occupies eight bytes and the 7 beside it occupies one.
//
// That asymmetry needs no branch, and the absence of one is the thing to read
// carefully rather than a case that was forgotten. Reading the bytes big-endian
// into a uint64 and converting once serves both: a narrower width leaves the
// high bytes zero, so the value is positive and inside int64 either way, while
// the eight-byte width reinterprets the same bits as the two's-complement
// number CoreFoundation wrote. A branch here would have two identical arms.
//
// The sixteen-byte form is refused. It exists and holds values above the range
// of an int64, which this value model cannot carry -- and silently truncating
// one would make two different numbers compare equal, which is worse in a drift
// check than declining to compare the file at all.
func (r *binaryReader) integer(offset, low int) (int64, error) {
	width := 1 << low
	if width > 8 {
		return 0, notAPlist("an integer of %d bytes is outside this reader's range", width)
	}
	if offset+1+width > len(r.data)-trailerSize {
		return 0, notAPlist("an integer at %d runs past the end of the objects", offset)
	}
	return int64(beUint(r.data[offset+1 : offset+1+width])), nil
}

// real reads the floating-point object whose marker is at offset.
func (r *binaryReader) real(offset, low int) (float64, error) {
	width := 1 << low
	if width != 4 && width != 8 {
		return 0, notAPlist("a real of %d bytes is not a property-list value", width)
	}
	return r.float(offset+1, width)
}

// float reads a big-endian IEEE 754 value of four or eight bytes.
//
// A four-byte real is widened to float64 rather than kept apart, because the
// value model has one floating-point type: the same number written by the XML
// side, which has no width at all, must compare equal to it.
func (r *binaryReader) float(at, width int) (float64, error) {
	if at+width > len(r.data)-trailerSize {
		return 0, notAPlist("a real at %d runs past the end of the objects", at)
	}
	if width == 4 {
		return float64(math.Float32frombits(binary.BigEndian.Uint32(r.data[at:]))), nil
	}
	return math.Float64frombits(binary.BigEndian.Uint64(r.data[at:])), nil
}

// beUint reads up to eight big-endian bytes as an unsigned value. The format
// stores offsets and references in whatever width the trailer declares, which
// is rarely one of the sizes encoding/binary has a function for.
func beUint(b []byte) uint64 {
	var value uint64
	for _, x := range b {
		value = value<<8 | uint64(x)
	}
	return value
}
