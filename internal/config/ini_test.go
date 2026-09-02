package config

import (
	"reflect"
	"testing"
)

func TestBareKeysAndKeyValueLinesAreBothAccepted(t *testing.T) {
	// appspec/03 "File format": within a section, "either `key = value` lines
	// or bare keys (a line with no `=`)". Both forms in one section, because
	// the application lists are bare-key sections and [storage] is not, and a
	// parser that handled only one of them still passes half the file.
	parsed := parseINI("[applications_to_sync]\nvim\ngit = anything\n")
	section := parsed.section(syncSection)
	if want := []string{"vim", "git"}; !reflect.DeepEqual(section.keys, want) {
		t.Errorf("keys = %v, want %v in the order they were written", section.keys, want)
	}
	if value, present := section.value("vim"); !present || value != "" {
		t.Errorf("the bare key vim = (%q, %v), want it present with an empty value", value, present)
	}
}

func TestKeysAreLowercasedAndValuesAreNot(t *testing.T) {
	// The case policy of appspec/03, which is one rule with two consequences:
	// application-list keys are normalized so a user may write "Vim", and
	// "[storage] values are case-sensitive" so an engine name is compared
	// exactly. A parser that folded values too would accept "Dropbox" as an
	// engine; one that preserved key case would fail to match "Vim".
	parsed := parseINI("[storage]\nENGINE = File_System\nPath = Some/Folder\n")
	section := parsed.section(storageSection)

	if value, present := section.value(engineKey); !present || value != "File_System" {
		t.Errorf("engine = (%q, %v), want the value preserved verbatim under a lowercased key", value, present)
	}
	if value, _ := section.value(pathKey); value != "Some/Folder" {
		t.Errorf("path = %q, want the value's case preserved", value)
	}
}

func TestSectionNamesAreMatchedExactly(t *testing.T) {
	// appspec/03: "Section presence is by exact name." So a differently-cased
	// header names a DIFFERENT section, which is then one of the unknown ones
	// the same paragraph says are ignored. This is what keeps the legacy
	// names -- which are capitalized and spaced -- distinguishable from
	// anything else.
	parsed := parseINI("[Storage]\nengine = icloud\n")
	if parsed.has(storageSection) {
		t.Error("[Storage] was read as [storage]; appspec/03 matches section names exactly")
	}
	if !parsed.has("Storage") {
		t.Error("[Storage] was not recorded at all")
	}
}

func TestCommentsAreStripped(t *testing.T) {
	// appspec/03: "text following ';' or '#' on a value line is treated as a
	// comment and stripped. Whole-line comments starting with ';' or '#' are
	// ignored." Both prefixes, in both positions, and on a bare key as well as
	// a value -- the bare-key case is the one a parser that stripped comments
	// only after splitting on '=' would miss.
	parsed := parseINI(`# a whole-line comment
; another one
[storage]   # after a header
engine = icloud   ; after a value
directory = Mackup # after another

[applications_to_sync]
vim   # after a bare key
`)
	section := parsed.section(storageSection)
	if value, _ := section.value(engineKey); value != "icloud" {
		t.Errorf("engine = %q, want %q with the comment stripped and the value trimmed", value, "icloud")
	}
	if value, _ := section.value(directoryKey); value != "Mackup" {
		t.Errorf("directory = %q, want %q", value, "Mackup")
	}
	if want := []string{"vim"}; !reflect.DeepEqual(parsed.section(syncSection).keys, want) {
		t.Errorf("the sync section holds %v, want %v", parsed.section(syncSection).keys, want)
	}
	if parsed.has("") || len(parsed.sections) != 2 {
		t.Errorf("the file parsed to %d sections, want the two that were written", len(parsed.sections))
	}
}

func TestAnUnknownSectionIsKeptAndIgnored(t *testing.T) {
	// appspec/03: "Unknown sections are ignored (with one exception: the two
	// legacy section names below abort the program)." The exception is why
	// the parser keeps them rather than dropping them: the legacy refusal is a
	// lookup for a section that is otherwise unrecognized, so a parser that
	// filtered to the four recognized names could not implement it.
	parsed := parseINI("[whatever]\nkey = value\n[" + legacyAllowedSection + "]\nvim\n")
	if !parsed.has("whatever") {
		t.Error("an unknown section was dropped by the parser")
	}
	if !parsed.has(legacyAllowedSection) {
		t.Errorf("[%s] was dropped by the parser, so the legacy refusal could never see it", legacyAllowedSection)
	}
}

func TestKeysWrittenBeforeAnySectionAreIgnored(t *testing.T) {
	// Not stated by appspec/03, and decided here rather than left implicit: a
	// key outside every section is in no recognized section, so it is ignored
	// exactly as a key in an unrecognized one is. The alternative -- treating
	// it as an error -- would invent a fatal condition appspec/07's table does
	// not list.
	parsed := parseINI("engine = icloud\n[storage]\nengine = file_system\n")
	if value, _ := parsed.section(storageSection).value(engineKey); value != "file_system" {
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
	parsed := parseINI("[applications_to_ignore]\nssh\n[storage]\nengine = icloud\n[applications_to_ignore]\ngnupg\n")
	if want := []string{"ssh", "gnupg"}; !reflect.DeepEqual(parsed.section(ignoreSection).keys, want) {
		t.Errorf("the ignore section holds %v, want %v", parsed.section(ignoreSection).keys, want)
	}
}

func TestARepeatedKeyTakesItsLastValue(t *testing.T) {
	parsed := parseINI("[storage]\ndirectory = first\ndirectory = second\n")
	section := parsed.section(storageSection)
	if value, _ := section.value(directoryKey); value != "second" {
		t.Errorf("directory = %q, want the last value written", value)
	}
	if want := []string{"directory"}; !reflect.DeepEqual(section.keys, want) {
		t.Errorf("keys = %v, want the repeated key listed once", section.keys)
	}
}

func TestAPresentKeyWithAnEmptyValueIsNotAnAbsentKey(t *testing.T) {
	// The distinction section.value reports, and the reason it reports two
	// results: appspec/03 gives "directory" a default when the key is ABSENT,
	// and accepts "any other value ... verbatim" when it is present. So
	// `directory =` is an empty sub-directory name, not a missing one, and a
	// parser that collapsed the two would silently apply the default.
	parsed := parseINI("[storage]\ndirectory =\n")
	value, present := parsed.section(storageSection).value(directoryKey)
	if !present {
		t.Error("directory was reported absent, but the key is written in the file")
	}
	if value != "" {
		t.Errorf("directory = %q, want the empty string", value)
	}
	if _, present := parsed.section(storageSection).value(engineKey); present {
		t.Error("engine was reported present, but the key is not in the file")
	}
}

func TestAnAbsentSectionAnswersEveryLookupAsAbsent(t *testing.T) {
	// section returns nil for a section the file does not have, and a nil
	// section must be safe to read: Load asks [storage] for three keys without
	// first checking that the section exists, because the specification gives
	// every one of them a default or makes it optional.
	parsed := parseINI("")
	if section := parsed.section(storageSection); section != nil {
		t.Fatalf("an absent section = %v, want nil", section)
	}
	if value, present := parsed.section(storageSection).value(engineKey); present || value != "" {
		t.Errorf("reading a key of an absent section = (%q, %v), want it absent", value, present)
	}
}

func TestWhitespaceAroundKeysAndValuesIsTrimmed(t *testing.T) {
	parsed := parseINI("  [storage]  \n\tengine   =   icloud  \n   \n\t vim-like  \n")
	section := parsed.section(storageSection)
	if value, present := section.value(engineKey); !present || value != "icloud" {
		t.Errorf("engine = (%q, %v), want a trimmed key and value", value, present)
	}
	if want := []string{"engine", "vim-like"}; !reflect.DeepEqual(section.keys, want) {
		t.Errorf("keys = %v, want %v: a blank line is not a key", section.keys, want)
	}
}

func TestALineHoldingOnlyABracketIsNotASectionHeader(t *testing.T) {
	// header() is what decides whether a line opens a section, and it is the
	// one place a malformed line could silently swallow the rest of the file
	// by opening a section nothing looks up.
	for _, line := range []string{"[", "]", "[]"} {
		parsed := parseINI("[storage]\n" + line + "\nengine = icloud\n")
		if value, _ := parsed.section(storageSection).value(engineKey); value != "icloud" {
			t.Errorf("after the line %q the engine read as %q; that line is not a section header", line, value)
		}
	}
}
