package plist

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// indent is one nesting level of the rendered structure.
const indent = "  "

// Format renders a parsed property list as the "pretty-printed structure"
// appspec/06-sync-operations.md takes the plist diff over: one line per scalar
// and per container delimiter, deepest structure indented.
//
// It is line-oriented because the thing built on top of it is a unified diff,
// which is defined over lines. A renderer that packed a dictionary onto one
// line would produce a diff whose every hunk was the whole file.
//
// Dictionary keys are sorted. A property-list dictionary is unordered -- both
// on-disk formats are free to write the same dictionary in any order, and the
// Go map this package parses into keeps none -- so an unsorted rendering would
// make the diff depend on something the file does not mean, and two files
// holding identical settings would differ.
//
// # The contract with the comparison built on it
//
// Equal renderings mean equal content. That is what lets a caller decide
// appspec/06's "identical" by comparing the rendered lines rather than by
// walking the values, and it is the property that keeps "identical" and "the
// diff is empty" from ever disagreeing -- a pair that compared as differing
// and then produced no diff detail would leave the user staring at a prompt
// with nothing under it.
//
// Every scalar below is therefore rendered so that no two values of different
// types, and no two distinct values of one type, can print the same: strings
// are quoted, reals always carry a decimal point or an exponent so that 1.0
// cannot print as the integer 1, data is hex inside angle brackets, and dates
// are RFC 3339 in UTC. An edit here that makes two values render alike is not
// a cosmetic change; it makes drift detection report "already in sync" for
// files that are not. TestTheRenderingTellsEveryValueApart is what holds that.
func Format(value any) []string {
	return render(nil, "", "", value)
}

// render appends the lines of one value, with prefix as its indentation and
// label leading its first line -- a dictionary key, or nothing for an array
// element or the root.
func render(out []string, prefix, label string, value any) []string {
	switch v := value.(type) {
	case map[string]any:
		if len(v) == 0 {
			return append(out, prefix+label+"{}")
		}
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out = append(out, prefix+label+"{")
		for _, key := range keys {
			out = render(out, prefix+indent, strconv.Quote(key)+": ", v[key])
		}
		return append(out, prefix+"}")
	case []any:
		if len(v) == 0 {
			return append(out, prefix+label+"[]")
		}
		out = append(out, prefix+label+"[")
		for _, element := range v {
			out = render(out, prefix+indent, "", element)
		}
		return append(out, prefix+"]")
	default:
		return append(out, prefix+label+scalar(value))
	}
}

// scalar renders one leaf value.
//
// The default arm cannot be reached while Parse is the only producer of these
// values -- the package doc's table is the whole set -- and it renders rather
// than panics because a drift check is not the place to take a sync run down
// over a value it could still show the user something about.
func scalar(value any) string {
	switch v := value.(type) {
	case string:
		return strconv.Quote(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return real(v)
	case bool:
		return strconv.FormatBool(v)
	case time.Time:
		// UTC, because the same instant written by two machines in two zones
		// is the same setting and must render the same. RFC3339Nano rather
		// than RFC3339 so that a sub-second difference is visible instead of
		// being rounded into agreement.
		return v.UTC().Format(time.RFC3339Nano)
	case []byte:
		// Angle brackets around lower-case hex: the spelling the OpenStep
		// plist format used for data, which is what a reader of this output is
		// most likely to recognise, and which no other value here can produce.
		return "<" + hex.EncodeToString(v) + ">"
	default:
		return fmt.Sprintf("%#v", value)
	}
}

// real renders a floating-point value so that it can never be mistaken for an
// integer.
//
// Go's shortest round-trip formatting is what makes the rendering injective
// among reals -- two different float64 values never produce the same text --
// but it prints a whole number as bare digits, which is exactly the integer
// spelling. A plist may hold 1 and 1.0 as different values in the same slot,
// and reporting them as identical is the failure this function exists to
// prevent, so a whole number is given the ".0" that says which type it is.
func real(value float64) string {
	text := strconv.FormatFloat(value, 'g', -1, 64)
	if strings.IndexFunc(text, func(r rune) bool { return r != '-' && (r < '0' || r > '9') }) < 0 {
		return text + ".0"
	}
	return text
}
