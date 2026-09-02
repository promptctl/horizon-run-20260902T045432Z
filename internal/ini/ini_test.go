package ini

import (
	"reflect"
	"testing"
)

// The section and key names the cases below use are the specification's, spelled
// here rather than imported: this package reads a dialect and does not know which
// sections mean anything, and a case that borrowed a constant from the package
// that does would be asserting on that package's names instead of on the parser.
const (
	storageSection = "storage"
	syncSection    = "applications_to_sync"
	ignoreSection  = "applications_to_ignore"

	engineKey    = "engine"
	directoryKey = "directory"
	pathKey      = "path"

	// legacyAllowedSection is one of the two pre-migration names of appspec/03,
	// capitalized and spaced. It appears here because it is the reason the
	// parser keeps unknown sections at all.
	legacyAllowedSection = "Allowed Applications"
)

func TestBareKeysAndKeyValueLinesAreBothAccepted(t *testing.T) {
	// appspec/03 "File format": within a section, "either `key = value` lines
	// or bare keys (a line with no `=`)". Both forms in one section, because
	// the application lists are bare-key sections and [storage] is not, and a
	// parser that handled only one of them still passes half the file.
	//
	// appspec/05 needs the same two shapes for the same reason: a definition's
	// path sections are bare keys and its [application] section is not.
	parsed := Parse("[applications_to_sync]\nvim\ngit = anything\n", LowercaseKeys)
	section := parsed.Section(syncSection)
	if want := []string{"vim", "git"}; !reflect.DeepEqual(section.keys, want) {
		t.Errorf("keys = %v, want %v in the order they were written", section.keys, want)
	}
	if value, present := section.Value("vim"); !present || value != "" {
		t.Errorf("the bare key vim = (%q, %v), want it present with an empty value", value, present)
	}
}

func TestKeysAreLowercasedAndValuesAreNot(t *testing.T) {
	// The case policy of appspec/03, which is one rule with two consequences:
	// application-list keys are normalized so a user may write "Vim", and
	// "[storage] values are case-sensitive" so an engine name is compared
	// exactly. A parser that folded values too would accept "Dropbox" as an
	// engine; one that preserved key case would fail to match "Vim".
	parsed := Parse("[storage]\nENGINE = File_System\nPath = Some/Folder\n", LowercaseKeys)
	section := parsed.Section(storageSection)

	if value, present := section.Value(engineKey); !present || value != "File_System" {
		t.Errorf("engine = (%q, %v), want the value preserved verbatim under a lowercased key", value, present)
	}
	if value, _ := section.Value(pathKey); value != "Some/Folder" {
		t.Errorf("path = %q, want the value's case preserved", value)
	}
}

func TestExactKeysPreservesTheCaseOfEveryKey(t *testing.T) {
	// The other half of the case-policy pair. appspec/05: "Key names here are
	// case-preserving (they are not lowercased, unlike the user-config
	// application lists in 03) so paths keep their exact case." A definition
	// read under LowercaseKeys would store ".Xresources" as ".xresources" and
	// the sync engine would look for a file that is not there.
	parsed := Parse("[configuration_files]\n.Xresources\n.config/Sub/File\n", ExactKeys)
	section := parsed.Section("configuration_files")
	if want := []string{".Xresources", ".config/Sub/File"}; !reflect.DeepEqual(section.Keys(), want) {
		t.Errorf("keys = %v, want %v with their case preserved", section.Keys(), want)
	}
}

func TestTheTwoKeyCasesDisagreeOnlyAboutKeys(t *testing.T) {
	// The parameter's whole scope, pinned: section names and values read the
	// same under both, because appspec/03 makes section presence exact and
	// [storage] values case-sensitive, and appspec/05 inherits both. A KeyCase
	// that reached any further would be a second dialect rather than one rule
	// with a documented difference.
	const content = "[Storage]\nENGINE = File_System\n"
	lowered := Parse(content, LowercaseKeys)
	exact := Parse(content, ExactKeys)

	for what, parsed := range map[string]*File{"LowercaseKeys": lowered, "ExactKeys": exact} {
		if !parsed.Has("Storage") || parsed.Has("storage") {
			t.Errorf("%s: the section name was folded; appspec/03 matches section names exactly", what)
		}
	}
	if value, _ := lowered.Section("Storage").Value("engine"); value != "File_System" {
		t.Errorf("under LowercaseKeys the value read as %q, want it preserved verbatim", value)
	}
	if value, _ := exact.Section("Storage").Value("ENGINE"); value != "File_System" {
		t.Errorf("under ExactKeys the value read as %q, want it preserved verbatim", value)
	}
	if _, present := exact.Section("Storage").Value("engine"); present {
		t.Error("under ExactKeys the key ENGINE answered to \"engine\"; the case is meant to be preserved")
	}
}

func TestKeysReturnsACopy(t *testing.T) {
	// The accessor hands out a copy, so a caller that sorts or truncates what
	// it gets does not reorder the parsed file for the next one. Both callers
	// build a set from it, and one of them sorts.
	parsed := Parse("[configuration_files]\nb\na\n", ExactKeys)
	section := parsed.Section("configuration_files")
	keys := section.Keys()
	keys[0] = "mutated"
	if want := []string{"b", "a"}; !reflect.DeepEqual(section.Keys(), want) {
		t.Errorf("after a caller wrote to the returned slice the section holds %v, want %v", section.Keys(), want)
	}
}

func TestSectionNamesAreMatchedExactly(t *testing.T) {
	// appspec/03: "Section presence is by exact name." So a differently-cased
	// header names a DIFFERENT section, which is then one of the unknown ones
	// the same paragraph says are ignored. This is what keeps the legacy
	// names -- which are capitalized and spaced -- distinguishable from
	// anything else.
	parsed := Parse("[Storage]\nengine = icloud\n", LowercaseKeys)
	if parsed.Has(storageSection) {
		t.Error("[Storage] was read as [storage]; appspec/03 matches section names exactly")
	}
	if !parsed.Has("Storage") {
		t.Error("[Storage] was not recorded at all")
	}
}

func TestCommentsAreStripped(t *testing.T) {
	// appspec/03: "text following ';' or '#' on a value line is treated as a
	// comment and stripped. Whole-line comments starting with ';' or '#' are
	// ignored." Both prefixes, in both positions, and on a bare key as well as
	// a value -- the bare-key case is the one a parser that stripped comments
	// only after splitting on '=' would miss.
	parsed := Parse(`# a whole-line comment
; another one
[storage]   # after a header
engine = icloud   ; after a value
directory = Mackup # after another

[applications_to_sync]
vim   # after a bare key
`, LowercaseKeys)
	section := parsed.Section(storageSection)
	if value, _ := section.Value(engineKey); value != "icloud" {
		t.Errorf("engine = %q, want %q with the comment stripped and the value trimmed", value, "icloud")
	}
	if value, _ := section.Value(directoryKey); value != "Mackup" {
		t.Errorf("directory = %q, want %q", value, "Mackup")
	}
	if want := []string{"vim"}; !reflect.DeepEqual(parsed.Section(syncSection).keys, want) {
		t.Errorf("the sync section holds %v, want %v", parsed.Section(syncSection).keys, want)
	}
	if parsed.Has("") || len(parsed.sections) != 2 {
		t.Errorf("the file parsed to %d sections, want the two that were written", len(parsed.sections))
	}
}

func TestAnUnknownSectionIsKeptAndIgnored(t *testing.T) {
	// appspec/03: "Unknown sections are ignored (with one exception: the two
	// legacy section names below abort the program)." The exception is why
	// the parser keeps them rather than dropping them: the legacy refusal is a
	// lookup for a section that is otherwise unrecognized, so a parser that
	// filtered to the four recognized names could not implement it. appspec/05
	// wants the same of a definition file, which lists three recognized
	// sections and says nothing about any other.
	parsed := Parse("[whatever]\nkey = value\n["+legacyAllowedSection+"]\nvim\n", LowercaseKeys)
	if !parsed.Has("whatever") {
		t.Error("an unknown section was dropped by the parser")
	}
	if !parsed.Has(legacyAllowedSection) {
		t.Errorf("[%s] was dropped by the parser, so the legacy refusal could never see it", legacyAllowedSection)
	}
}

func TestKeysWrittenBeforeAnySectionAreIgnored(t *testing.T) {
	// Not stated by appspec/03, and decided here rather than left implicit: a
	// key outside every section is in no recognized section, so it is ignored
	// exactly as a key in an unrecognized one is. The alternative -- treating
	// it as an error -- would invent a fatal condition appspec/07's table does
	// not list.
	parsed := Parse("engine = icloud\n[storage]\nengine = file_system\n", LowercaseKeys)
	if value, _ := parsed.Section(storageSection).Value(engineKey); value != "file_system" {
		t.Errorf("engine = %q, want the one written inside [storage]", value)
	}
	if len(parsed.sections) != 1 {
		t.Errorf("the file parsed to %d sections, want only [storage]", len(parsed.sections))
	}
}

func TestARepeatedSectionHeaderContinuesTheSection(t *testing.T) {
	// A config that opens the same section twice lists the union of both
	// blocks. Nothing in appspec/03 says otherwise, and the alternative
	// silently discards whichever half the user wrote first -- which for an
	// ignore list means syncing files they asked not to sync.
	parsed := Parse("[applications_to_ignore]\nssh\n[storage]\nengine = icloud\n[applications_to_ignore]\ngnupg\n", LowercaseKeys)
	if want := []string{"ssh", "gnupg"}; !reflect.DeepEqual(parsed.Section(ignoreSection).keys, want) {
		t.Errorf("the ignore section holds %v, want %v", parsed.Section(ignoreSection).keys, want)
	}
}

func TestARepeatedKeyTakesItsLastValue(t *testing.T) {
	parsed := Parse("[storage]\ndirectory = first\ndirectory = second\n", LowercaseKeys)
	section := parsed.Section(storageSection)
	if value, _ := section.Value(directoryKey); value != "second" {
		t.Errorf("directory = %q, want the last value written", value)
	}
	if want := []string{"directory"}; !reflect.DeepEqual(section.keys, want) {
		t.Errorf("keys = %v, want the repeated key listed once", section.keys)
	}
}

func TestAPresentKeyWithAnEmptyValueIsNotAnAbsentKey(t *testing.T) {
	// The distinction Value reports, and the reason it reports two results:
	// appspec/03 gives "directory" a default when the key is ABSENT, and
	// accepts "any other value ... verbatim" when it is present. So
	// `directory =` is an empty sub-directory name, not a missing one, and a
	// parser that collapsed the two would silently apply the default.
	parsed := Parse("[storage]\ndirectory =\n", LowercaseKeys)
	value, present := parsed.Section(storageSection).Value(directoryKey)
	if !present {
		t.Error("directory was reported absent, but the key is written in the file")
	}
	if value != "" {
		t.Errorf("directory = %q, want the empty string", value)
	}
	if _, present := parsed.Section(storageSection).Value(engineKey); present {
		t.Error("engine was reported present, but the key is not in the file")
	}
}

func TestAnAbsentSectionAnswersEveryLookupAsAbsent(t *testing.T) {
	// Section returns nil for a section the file does not have, and a nil
	// section must be safe to read: config.Load asks [storage] for three keys
	// without first checking that the section exists, because the specification
	// gives every one of them a default or makes it optional, and the
	// application database asks a definition for two sections appspec/05 says
	// may both be absent.
	parsed := Parse("", LowercaseKeys)
	if section := parsed.Section(storageSection); section != nil {
		t.Fatalf("an absent section = %v, want nil", section)
	}
	if value, present := parsed.Section(storageSection).Value(engineKey); present || value != "" {
		t.Errorf("reading a key of an absent section = (%q, %v), want it absent", value, present)
	}
	if keys := parsed.Section(storageSection).Keys(); keys != nil {
		t.Errorf("the keys of an absent section = %v, want none", keys)
	}
}

func TestWhitespaceAroundKeysAndValuesIsTrimmed(t *testing.T) {
	parsed := Parse("  [storage]  \n\tengine   =   icloud  \n   \n\t vim-like  \n", LowercaseKeys)
	section := parsed.Section(storageSection)
	if value, present := section.Value(engineKey); !present || value != "icloud" {
		t.Errorf("engine = (%q, %v), want a trimmed key and value", value, present)
	}
	if want := []string{"engine", "vim-like"}; !reflect.DeepEqual(section.keys, want) {
		t.Errorf("keys = %v, want %v: a blank line is not a key", section.keys, want)
	}
}

func TestALineHoldingOnlyABracketIsNotASectionHeader(t *testing.T) {
	// header is what decides whether a line opens a section, and it is the
	// one place a malformed line could silently swallow the rest of the file
	// by opening a section nothing looks up.
	for _, line := range []string{"[", "]", "[]"} {
		parsed := Parse("[storage]\n"+line+"\nengine = icloud\n", LowercaseKeys)
		if value, _ := parsed.Section(storageSection).Value(engineKey); value != "icloud" {
			t.Errorf("after the line %q the engine read as %q; that line is not a section header", line, value)
		}
	}
}
