// Package ini reads the INI-style file format the specification uses for both
// kinds of file it defines: the user config of appspec/03-configuration.md and
// the application definitions of appspec/05-application-database.md.
//
// One dialect, two readings. appspec/03 describes the format in detail --
// "[section]" headers, "key = value" lines, bare keys, ";" and "#" comments --
// and appspec/05 says each definition is "one INI-style .cfg file" with the
// same shapes. The single documented difference between them is the case of
// KEYS, and the two specifications state it as one paired rule: appspec/03's
// "config application-list keys are case-normalized; definition file paths are
// case-exact", repeated in appspec/05 as "one half of the case-policy pair".
// So the case of keys is the only thing Parse takes an argument for, and
// everything else about the dialect exists once.
//
// The alternative was a second parser in the package that reads definitions.
// It would have been sixty lines that agree today and are asked to keep
// agreeing about comments, whitespace, repeated headers and malformed lines --
// none of which either specification distinguishes between the two file kinds.
// A divergence there does not look like a bug: it looks like a config file that
// means one thing and a definition file that means another.
//
// Parsing cannot fail. Nothing in appspec/03 or appspec/05 makes a malformed
// line fatal; the conditions they do make fatal -- a legacy section, a bad
// engine, an absolute path in a definition -- are decided by reading the parsed
// result, not by parsing it.
package ini

import "strings"

// A KeyCase is how Parse treats the case of key names.
//
// It is the case-policy pair of appspec/03 and appspec/05 expressed as the one
// parameter the dialect has. Values, and section names, are never folded under
// either: appspec/03 makes "[storage] values case-sensitive" and section
// "presence is by exact name", and appspec/05 depends on both.
type KeyCase int

const (
	// LowercaseKeys normalizes key names to lowercase, which appspec/03
	// requires of the user config: "Names in [applications_to_sync] and
	// [applications_to_ignore] are normalized to lowercase by the parser." The
	// specification says nothing that distinguishes [storage] KEYS from those,
	// so one rule covers the whole file.
	LowercaseKeys KeyCase = iota
	// ExactKeys preserves key names verbatim, which appspec/05 requires of an
	// application definition: "Key names here are case-preserving (they are not
	// lowercased, unlike the user-config application lists in 03) so paths keep
	// their exact case."
	//
	// It applies to the whole definition file and not only to its path
	// sections, which is a reading worth stating: the "name" key of
	// [application] is matched exactly too, so a definition writing "Name =" has
	// no name. The case policy is a property of how the file is read, and a
	// parser that folded one section's keys and not another's would be a third
	// dialect that neither specification describes.
	ExactKeys
)

// A Section is one "[name]" block of a file.
//
// It keeps its keys in the order they were written as well as by name, because
// the bare-key sections of both file kinds are LISTS -- an application list is
// read as a set, and a definition's file set is a set too, but a diagnostic
// that names one member should show the user their own file rather than a map's
// iteration order.
type Section struct {
	keys   []string
	values map[string]string
}

// Value returns the value written for a key, and whether the key was present at
// all. The two differ: appspec/03 gives "directory" a default when the key is
// absent and accepts "any other value ... verbatim" when it is present, so
// `directory =` is an empty sub-directory name and not a missing one.
func (s *Section) Value(key string) (string, bool) {
	if s == nil {
		return "", false
	}
	v, ok := s.values[key]
	return v, ok
}

// Keys returns the section's key names in the order they were written, with a
// repeated key listed once at its first position.
//
// A copy, so a caller cannot reorder or truncate the parsed file underneath
// another one. Both callers walk it to build a set, and the cost of the copy is
// one small slice per section of a file that was just read from disk.
func (s *Section) Keys() []string {
	if s == nil {
		return nil
	}
	keys := make([]string, len(s.keys))
	copy(keys, s.keys)
	return keys
}

// A File is a parsed file: its sections, by the exact name each was written
// with.
//
// appspec/03: "Section presence is by exact name." So the map is keyed by the
// literal text between the brackets -- "[Storage]" is not "[storage]", and
// neither is a recognized section. Only the KEYS inside a section are subject
// to the case policy above.
type File struct {
	sections map[string]*Section
}

// Section returns the named section, or nil if the file has none. A nil section
// answers every lookup with "absent", so a caller reads a missing section and an
// empty one through the same code.
func (f *File) Section(name string) *Section { return f.sections[name] }

// Has reports whether the file contains a section with this exact name, empty or
// not. appspec/03's legacy-section refusal turns on presence alone.
func (f *File) Has(name string) bool {
	_, ok := f.sections[name]
	return ok
}

// Parse reads content in the dialect described above, treating key names as the
// given KeyCase says.
//
// Unknown sections are parsed and kept rather than dropped: dropping them would
// mean this function had to know which section names matter to which caller, and
// appspec/03's legacy-section refusal is precisely a lookup for a section that is
// otherwise unrecognized.
//
// A key written before any section header belongs to no section and is ignored,
// the same as one in a section nothing looks up. Neither specification says
// otherwise, and the alternative -- an error -- would invent a fatal condition
// appspec/07's table does not list.
func Parse(content string, keys KeyCase) *File {
	parsed := &File{sections: map[string]*Section{}}
	var current *Section

	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(stripComment(line))
		if line == "" {
			continue
		}
		if name, ok := header(line); ok {
			if parsed.sections[name] == nil {
				parsed.sections[name] = &Section{values: map[string]string{}}
			}
			// A repeated header continues the section it names rather than
			// replacing it, so a config that opens "[applications_to_ignore]"
			// twice lists the union of both blocks. Nothing in appspec/03 says
			// otherwise, and the alternative silently discards the keys the
			// user wrote first.
			current = parsed.sections[name]
			continue
		}
		if current == nil {
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if keys == LowercaseKeys {
			key = strings.ToLower(key)
		}
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
// looks up and swallow every key after it, which is the one way a malformed line
// here can silently change what the file means.
func header(line string) (string, bool) {
	if len(line) < 3 || line[0] != '[' || line[len(line)-1] != ']' {
		return "", false
	}
	name := strings.TrimSpace(line[1 : len(line)-1])
	return name, name != ""
}

// stripComment removes an inline or whole-line comment from a line.
//
// appspec/03 states the rule without qualification: "text following ';' or '#'
// on a value line is treated as a comment and stripped. Whole-line comments
// starting with ';' or '#' are ignored." So the first of either character ends
// the line, wherever it appears -- there is no exception for one embedded in a
// word, and neither a storage path nor a definition's file path containing a '#'
// can be written. That is the specification's rule rather than this parser's
// preference, and narrowing it would silently change which paths a file can
// name.
func stripComment(line string) string {
	if at := strings.IndexAny(line, ";#"); at >= 0 {
		return line[:at]
	}
	return line
}
