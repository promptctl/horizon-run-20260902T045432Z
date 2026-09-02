package plist

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// parseXML reads the XML spelling of a property list: a <plist> document
// element wrapping exactly one value.
//
// The document element is required, and that requirement is the whole
// classification. Every other XML document -- well-formed or not, however
// plist-like its contents -- is rejected here, so appspec/06's plist arm
// cannot capture a file the user thinks of as text. A <plist> element holding
// no value, or more than one, is rejected for the same reason: it is not a
// property list, and guessing which child was meant would be inventing content
// to diff.
func parseXML(data []byte) (any, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))

	start, err := documentElement(decoder)
	if err != nil {
		return nil, err
	}
	if start.Name.Local != "plist" {
		return nil, notAPlist("document element is <%s>, not <plist>", start.Name.Local)
	}

	value, ok, err := nextValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notAPlist("<plist> holds no value")
	}
	if _, more, err := nextValue(decoder, 0); err != nil {
		return nil, err
	} else if more {
		return nil, notAPlist("<plist> holds more than one value")
	}
	return value, nil
}

// documentElement advances the decoder to the first start element, skipping
// the prolog.
//
// The prolog is skipped by KIND rather than by name: the XML declaration is a
// ProcInst, the DOCTYPE a Directive, and the newlines between them CharData,
// and a plist written by CoreFoundation carries all three. Comments before the
// document element are legal XML and are skipped for the same reason.
func documentElement(decoder *xml.Decoder) (xml.StartElement, error) {
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return xml.StartElement{}, notAPlist("no XML document element")
		}
		if err != nil {
			return xml.StartElement{}, notAPlist("%v", err)
		}
		if start, isStart := token.(xml.StartElement); isStart {
			return start, nil
		}
	}
}

// nextValue reads the next value element at the current nesting level,
// reporting false when the enclosing element ends first. depth is how many
// containers enclose it; see maxDepth.
//
// Character data between elements is skipped, because the indentation
// CoreFoundation writes is character data and carries no meaning inside a
// container. That is not true INSIDE a <string> or a <data>, which are read by
// element below rather than through this function.
func nextValue(decoder *xml.Decoder, depth int) (any, bool, error) {
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, notAPlist("%v", err)
		}
		switch t := token.(type) {
		case xml.StartElement:
			value, err := element(decoder, t, depth)
			return value, err == nil, err
		case xml.EndElement:
			return nil, false, nil
		}
	}
}

// element reads one value element, whose start tag has already been consumed,
// at a nesting depth of depth containers.
//
// The type of the element IS the type of the value -- that is what the XML
// format is -- so this switch is the mapping the package doc tabulates, and an
// element outside it makes the document not a property list rather than an
// empty value.
func element(decoder *xml.Decoder, start xml.StartElement, depth int) (any, error) {
	if depth >= maxDepth {
		return nil, notAPlist("property list nests more than %d deep", maxDepth)
	}
	switch start.Name.Local {
	case "dict":
		return xmlDict(decoder, depth+1)
	case "array":
		return xmlArray(decoder, depth+1)
	case "true", "false":
		if err := decoder.Skip(); err != nil {
			return nil, notAPlist("%v", err)
		}
		return start.Name.Local == "true", nil
	}

	text, err := text(decoder, start)
	if err != nil {
		return nil, err
	}
	switch start.Name.Local {
	case "string":
		// Not trimmed: leading and trailing whitespace inside a <string> is
		// part of the string, and a config value that is a single space or an
		// indented block would otherwise be silently rewritten by the reader
		// that is supposed to be reporting on it.
		return text, nil
	case "integer":
		number, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err != nil {
			return nil, notAPlist("<integer>%s</integer>: %v", text, err)
		}
		return number, nil
	case "real":
		number, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil {
			return nil, notAPlist("<real>%s</real>: %v", text, err)
		}
		return number, nil
	case "date":
		// RFC 3339 with a Z, which is the only spelling CoreFoundation
		// writes. A date is read into a time.Time rather than kept as text so
		// that it compares as an instant: the same moment written by two
		// machines is the same setting.
		when, err := time.Parse(time.RFC3339, strings.TrimSpace(text))
		if err != nil {
			return nil, notAPlist("<date>%s</date>: %v", text, err)
		}
		return when.UTC(), nil
	case "data":
		// Whitespace is stripped first: CoreFoundation wraps base64 payloads
		// across indented lines, and the encoding/base64 decoder rejects the
		// newlines rather than ignoring them.
		encoded := strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, text)
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, notAPlist("<data>: %v", err)
		}
		return decoded, nil
	}
	return nil, notAPlist("<%s> is not a property-list value", start.Name.Local)
}

// xmlDict reads a <dict>: alternating <key> elements and value elements.
//
// A repeated key keeps the last value, which is what the map model gives for
// free and matches CoreFoundation's own reader. The alternation is enforced --
// a value where a <key> belongs makes the document not a plist -- because a
// reader that resynchronised there would produce a structure the file does not
// describe and then diff it.
func xmlDict(decoder *xml.Decoder, depth int) (any, error) {
	dict := map[string]any{}
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, notAPlist("%v", err)
		}
		switch t := token.(type) {
		case xml.EndElement:
			return dict, nil
		case xml.StartElement:
			if t.Name.Local != "key" {
				return nil, notAPlist("<%s> where a <key> was expected in a <dict>", t.Name.Local)
			}
			key, err := text(decoder, t)
			if err != nil {
				return nil, err
			}
			value, ok, err := nextValue(decoder, depth)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, notAPlist("<key>%s</key> has no value", key)
			}
			dict[key] = value
		}
	}
}

// xmlArray reads an <array>: a sequence of value elements.
func xmlArray(decoder *xml.Decoder, depth int) (any, error) {
	// Non-nil so that an empty <array> renders as an empty array rather than
	// as a missing one; Format distinguishes the two and nothing else would
	// notice the difference.
	array := []any{}
	for {
		value, ok, err := nextValue(decoder, depth)
		if err != nil {
			return nil, err
		}
		if !ok {
			return array, nil
		}
		array = append(array, value)
	}
}

// text reads the character data of an element whose start tag has been
// consumed, through its end tag.
//
// DecodeElement rather than a token loop, so that entity references and CDATA
// sections in a <string> are resolved by the XML reader rather than by this
// one. It also consumes the end tag, which is what keeps the callers' loops in
// step.
func text(decoder *xml.Decoder, start xml.StartElement) (string, error) {
	var content string
	if err := decoder.DecodeElement(&content, &start); err != nil {
		return "", notAPlist("<%s>: %v", start.Name.Local, err)
	}
	return content, nil
}
