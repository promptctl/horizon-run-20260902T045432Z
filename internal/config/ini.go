package config

import "strings"

// A section is one "[name]" block of a config file.
//
// It keeps its keys in the order they were written as well as by name, because
// the two recognized bare-key sections are LISTS -- an application list is
// read as a set, but a diagnostic that names one, and any future output that
// echoes the config back, should show the user their own file rather than a
// map's iteration order.
type section struct {
	keys   []string
	values map[string]string
}

// value returns the value written for a key, and whether the key was present
// at all. The two differ: appspec/03 gives "directory" a default when the key
// is absent, and accepts "any other value ... verbatim" when it is present, so
// `directory =` is an empty sub-directory name and not a missing one.
func (s *section) value(key string) (string, bool) {
	if s == nil {
		return "", false
	}
	v, ok := s.values[key]
	return v, ok
}

// A file is a parsed config file: its sections, by the exact name each was
// written with.
//
// appspec/03: "Section presence is by exact name." So the map is keyed by the
// literal text between the brackets -- "[Storage]" is not "[storage]", and
// neither is a recognized section. Only the KEYS inside a section are
// case-normalized, which is the case-policy half appspec/03 pairs with
// appspec/05's case-exact definition file paths.
type file struct {
	sections map[string]*section
}

// section returns the named section, or nil if the file has none. A nil
// section answers every lookup with "absent", so a caller reads a missing
// section and an empty one through the same code.
func (f *file) section(name string) *section { return f.sections[name] }

// has reports whether the file contains a section with this exact name, empty
// or not. Used for the legacy-section refusal, which turns on presence alone.
func (f *file) has(name string) bool {
	_, ok := f.sections[name]
	return ok
}

// parseINI reads the config file format of appspec/03.
//
// The rules it implements, each stated there:
//
//   - "[section]" headers, and within a section either "key = value" lines or
//     bare keys -- "bare keys are permitted (values are optional)".
//   - Text following ";" or "#" is a comment and is stripped; a line that is
//     only a comment disappears.
//   - Keys are lowercased. appspec/03 requires it of the application lists and
//     stresses that "[storage] values are case-sensitive", saying nothing that
//     distinguishes storage KEYS -- so one rule covers both, which is also how
//     the reference's INI reader behaves.
//   - Unknown sections are ignored. They are parsed and kept anyway: dropping
//     them here would mean this function had to know which four names matter,
//     and the legacy-section refusal is precisely a lookup for a section that
//     is otherwise unrecognized.
//
// It cannot fail. Nothing in appspec/03 makes a malformed line fatal, and the
// only conditions it does make fatal -- a legacy section, a bad engine, a
// forbidden directory -- are decided by reading the parsed result, not by
// parsing. A key written before any section header therefore belongs to no
// section and is ignored, the same as one in a section nothing looks up.
func parseINI(content string) *file {
	parsed := &file{sections: map[string]*section{}}
	var current *section

	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(stripComment(line))
		if line == "" {
			continue
		}
		if name, ok := header(line); ok {
			if parsed.sections[name] == nil {
				parsed.sections[name] = &section{values: map[string]string{}}
			}
			// A repeated header continues the section it names rather than
			// replacing it, so a config that opens "[applications_to_ignore]"
			// twice lists the union of both blocks. Nothing in appspec/03
			// says otherwise, and the alternative silently discards the keys
			// the user wrote first.
			current = parsed.sections[name]
			continue
		}
		if current == nil {
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		if _, repeated := current.values[key]; !repeated {
			current.keys = append(current.keys, key)
		}
		current.values[key] = strings.TrimSpace(value)
	}
	return parsed
}

// header returns the section name a line declares, and whether it declares one.
//
// A header must NAME something: "[]" is not an anonymous section, it is a line
// that is not a header at all. Accepting it would open a section nothing ever
// looks up and swallow every key after it, which is the one way a malformed
// line here can silently change what the file means.
func header(line string) (string, bool) {
	if len(line) < 3 || line[0] != '[' || line[len(line)-1] != ']' {
		return "", false
	}
	name := strings.TrimSpace(line[1 : len(line)-1])
	return name, name != ""
}

// stripComment removes an inline or whole-line comment from a config line.
//
// appspec/03 states the rule without qualification: "text following ';' or '#'
// on a value line is treated as a comment and stripped. Whole-line comments
// starting with ';' or '#' are ignored." So the first of either character ends
// the line, wherever it appears -- there is no exception for one embedded in a
// word, and a storage path containing a '#' cannot be written here. That is
// the specification's rule rather than this parser's preference, and narrowing
// it would silently change which paths a config can name.
func stripComment(line string) string {
	if at := strings.IndexAny(line, ";#"); at >= 0 {
		return line[:at]
	}
	return line
}
