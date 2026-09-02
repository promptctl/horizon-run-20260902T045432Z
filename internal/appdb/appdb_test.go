package appdb

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/promptctl/macklebox/internal/catalog"
	"github.com/promptctl/macklebox/internal/fault"
)

// The cases below state the environment they are about rather than inheriting
// the developer's: every one builds a home directory of its own and passes it
// in. That is what Environment is for, and it is not a formality -- a case that
// read the real $HOME would see whatever definitions the developer keeps in
// ~/.mackup, and the built-in vim definition it asserts against would be
// shadowed on their machine and not on anyone else's.

// world is one throwaway home directory.
type world struct {
	t    *testing.T
	home string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	return &world{t: t, home: t.TempDir()}
}

// write creates a home-relative file, making its parent directories.
func (w *world) write(relative, content string) string {
	w.t.Helper()
	path := filepath.Join(w.home, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		w.t.Fatalf("creating the parent of %s: %v", relative, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		w.t.Fatalf("writing %s: %v", relative, err)
	}
	return path
}

// assemble builds the database for this world, failing the case if it cannot.
func (w *world) assemble(xdgConfigHome string) *Database {
	w.t.Helper()
	db, err := Assemble(Environment{Home: w.home, XDGConfigHome: xdgConfigHome})
	if err != nil {
		w.t.Fatalf("assembling the database: %v", err)
	}
	return db
}

// refuse builds the database expecting it to fail, and returns the diagnostic.
func (w *world) refuse(xdgConfigHome string) error {
	w.t.Helper()
	db, err := Assemble(Environment{Home: w.home, XDGConfigHome: xdgConfigHome})
	if err == nil {
		w.t.Fatalf("the database assembled with %d applications; want a refusal", len(db.Keys()))
	}
	return err
}

// definition writes one definition file's content.
func definition(name string, configurationFiles, xdgFiles []string) string {
	text := "[application]\nname = " + name + "\n"
	if len(configurationFiles) > 0 {
		text += "\n[configuration_files]\n" + strings.Join(configurationFiles, "\n") + "\n"
	}
	if len(xdgFiles) > 0 {
		text += "\n[xdg_configuration_files]\n" + strings.Join(xdgFiles, "\n") + "\n"
	}
	return text
}

// builtinKeys reads the keys of the built-in definition directory straight from
// internal/catalog, rather than restating a count.
//
// The number is the appendix's and internal/catalog's tests are what pin it
// there; a copy here would agree with itself while the two drifted. What these
// cases need is "the built-in set, plus or minus what this case added", which is
// a claim about assembly and not about the data.
func builtinKeys(t *testing.T) []string {
	t.Helper()
	entries, err := fs.ReadDir(catalog.Definitions(), ".")
	if err != nil {
		t.Fatalf("reading the built-in definition directory: %v", err)
	}
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, strings.TrimSuffix(entry.Name(), ".cfg"))
	}
	sort.Strings(keys)
	return keys
}

func TestTheBuiltInDirectoryIsTheDatabaseWhenNoUserDirectoryExists(t *testing.T) {
	// appspec/05's third tier, and the "A user directory that does not exist is
	// simply skipped" rule read together: a home with neither ~/.mackup nor an
	// XDG apps directory still has every shipped application.
	world := newWorld(t)
	db := world.assemble("")

	if got, want := db.Keys(), builtinKeys(t); !reflect.DeepEqual(got, want) {
		t.Errorf("the database holds %d keys, want the %d shipped built-in ones", len(got), len(want))
	}
	if name, ok := db.Name("vim"); !ok || name == "" {
		t.Errorf("the built-in vim definition read as (%q, %v), want a display name", name, ok)
	}
}

func TestKeysAreSortedAscending(t *testing.T) {
	// appspec/05 "Enumeration": list prints the keys "sorted ascending". A set
	// has no order of its own, so the database returns the one enumeration
	// asks for -- which is also what makes a refusal name the same definition
	// on two machines.
	world := newWorld(t)
	world.write(".mackup/zzz-last.cfg", definition("Last", nil, nil))
	world.write(".mackup/000-first.cfg", definition("First", nil, nil))
	keys := world.assemble("").Keys()

	if !sort.StringsAreSorted(keys) {
		t.Error("the keys are not sorted ascending")
	}
	if keys[0] != "000-first" || keys[len(keys)-1] != "zzz-last" {
		t.Errorf("the keys run from %q to %q, want them sorted around the built-in set", keys[0], keys[len(keys)-1])
	}
}

func TestADroppedDefinitionAddsAnApplication(t *testing.T) {
	// appspec/05 "Observed effects of adding a custom definition": "Dropping
	// ~/.mackup/myapp.cfg makes key myapp appear in list, increments the
	// supported-count trailer by one, makes show myapp print its display name
	// and file paths".
	world := newWorld(t)
	world.write(".mackup/myapp.cfg", definition("My App", []string{".myapprc", ".myapp"}, nil))
	db := world.assemble("")

	if got, want := len(db.Keys()), len(builtinKeys(t))+1; got != want {
		t.Errorf("the database holds %d keys, want %d: one more than the built-in set", got, want)
	}
	if name, ok := db.Name("myapp"); !ok || name != "My App" {
		t.Errorf("Name(\"myapp\") = (%q, %v), want (%q, true)", name, ok, "My App")
	}
	files, ok := db.Files("myapp")
	if want := []string{".myapp", ".myapprc"}; !ok || !reflect.DeepEqual(files, want) {
		t.Errorf("Files(\"myapp\") = (%v, %v), want %v sorted ascending", files, ok, want)
	}
}

func TestAUserDefinitionReplacesTheBuiltInOneEntirely(t *testing.T) {
	// appspec/05: "Dropping ~/.mackup/vim.cfg replaces the built-in vim
	// definition entirely (the built-in vim.cfg is not read at all for that
	// key)." Replaced, not merged -- the built-in file set must not survive
	// alongside the user's, and the key count must not change either.
	world := newWorld(t)
	world.write(".mackup/vim.cfg", definition("My Vim", []string{".my-vimrc"}, nil))
	db := world.assemble("")

	if got, want := len(db.Keys()), len(builtinKeys(t)); got != want {
		t.Errorf("the database holds %d keys, want the built-in %d: an override is not a new application", got, want)
	}
	if name, _ := db.Name("vim"); name != "My Vim" {
		t.Errorf("Name(\"vim\") = %q, want the user's display name", name)
	}
	files, _ := db.Files("vim")
	if want := []string{".my-vimrc"}; !reflect.DeepEqual(files, want) {
		t.Errorf("Files(\"vim\") = %v, want exactly %v: the built-in file set must not survive", files, want)
	}
}

func TestTheLegacyDirectoryOutranksTheXDGDirectory(t *testing.T) {
	// appspec/05's precedence, tier 1 over tier 2: a *.cfg in the XDG apps
	// directory is taken "only if a file of the same name was not already taken
	// from the legacy directory".
	world := newWorld(t)
	world.write(".mackup/myapp.cfg", definition("From Legacy", nil, nil))
	world.write(".config/mackup/applications/myapp.cfg", definition("From XDG", nil, nil))

	if name, _ := world.assemble("").Name("myapp"); name != "From Legacy" {
		t.Errorf("Name(\"myapp\") = %q, want the definition from ~/.mackup", name)
	}
}

func TestTheXDGDirectoryOutranksTheBuiltInOne(t *testing.T) {
	// Tier 2 over tier 3, which is the half a program that only looked in
	// ~/.mackup would still pass every other precedence case with.
	world := newWorld(t)
	world.write(".config/mackup/applications/vim.cfg", definition("From XDG", nil, nil))

	if name, _ := world.assemble("").Name("vim"); name != "From XDG" {
		t.Errorf("Name(\"vim\") = %q, want the definition from the XDG apps directory", name)
	}
}

func TestAShadowedDefinitionIsNotReadAtAll(t *testing.T) {
	// The strong form of appspec/05's "the built-in vim.cfg is not read at all
	// for that key", and the reason discovery resolves filenames before any file
	// is opened. A losing definition holding an absolute path would abort every
	// command if it were parsed, so a database that assembles here is evidence
	// the file was never read -- not merely that its contents lost a merge.
	world := newWorld(t)
	world.write(".config/mackup/applications/myapp.cfg", definition("Poisoned", []string{"/etc/passwd"}, nil))
	world.write(".mackup/myapp.cfg", definition("From Legacy", nil, nil))

	if name, _ := world.assemble("").Name("myapp"); name != "From Legacy" {
		t.Errorf("Name(\"myapp\") = %q, want the definition from ~/.mackup", name)
	}
}

func TestOnlyCfgFilesDirectlyInADirectoryAreDefinitions(t *testing.T) {
	// appspec/05 "Discovery": "Every *.cfg file directly in this directory is
	// taken ... Only files ending in .cfg are considered; other files are
	// ignored."
	//
	// Every fixture here holds an absolute path, so a reader that took any one
	// of them would abort assembly rather than quietly adding a key -- the
	// failure is loud and names which one.
	world := newWorld(t)
	poisoned := definition("Not A Definition", []string{"/etc/passwd"}, nil)
	world.write(".mackup/notes.txt", poisoned)
	world.write(".mackup/sub/nested.cfg", poisoned)
	world.write(".mackup/upper.CFG", poisoned)
	world.write(".mackup/.hidden.cfg", poisoned)
	if err := os.MkdirAll(filepath.Join(world.home, ".mackup", "adirectory.cfg"), 0o700); err != nil {
		t.Fatalf("creating a directory named like a definition: %v", err)
	}

	db := world.assemble("")
	if got, want := len(db.Keys()), len(builtinKeys(t)); got != want {
		t.Errorf("the database holds %d keys, want the built-in %d", got, want)
	}
	for _, key := range []string{"notes.txt", "notes", "nested", "upper", ".hidden", "adirectory"} {
		if _, ok := db.Name(key); ok {
			t.Errorf("%q was taken as an application key", key)
		}
	}
}

func TestASymlinkToADefinitionIsADefinition(t *testing.T) {
	// The other side of the regular-file test: a symlink resolves to a regular
	// file, and appspec/03's discovery reads a config candidate the same way --
	// "the file the user pointed at is the file that gets read". A user keeping
	// their definitions in a synced folder and linking them into ~/.mackup is
	// the ordinary reason to care.
	world := newWorld(t)
	target := world.write("definitions/myapp.cfg", definition("Linked", nil, nil))
	link := filepath.Join(world.home, ".mackup", "myapp.cfg")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatalf("creating ~/.mackup: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("linking a definition into ~/.mackup: %v", err)
	}

	if name, ok := world.assemble("").Name("myapp"); !ok || name != "Linked" {
		t.Errorf("Name(\"myapp\") = (%q, %v), want the linked definition", name, ok)
	}
}

func TestTheKeyIsTheFilenameAndKeepsItsCase(t *testing.T) {
	// appspec/05: "the file's basename without the .cfg extension is the
	// application key". Not lowercased: appspec/03 lowercases the CONFIG
	// application lists and says of the pair that "built-in application keys are
	// already lowercase, so effectively the listed names must match the
	// lowercase keys shown by list" -- which is a statement about the data, not
	// a normalization the database performs.
	world := newWorld(t)
	world.write(".mackup/MyApp.cfg", definition("My App", nil, nil))
	db := world.assemble("")

	if _, ok := db.Name("MyApp"); !ok {
		t.Error("the key MyApp is not in the database; the key is the filename as written")
	}
	if _, ok := db.Name("myapp"); ok {
		t.Error("the key was lowercased; appspec/05 makes the basename the key")
	}
}

func TestPathsKeepTheirExactCase(t *testing.T) {
	// The half of appspec/03's case-policy pair that appspec/05 owns:
	// "definition file paths are case-exact". A database that folded them would
	// send the sync engine after a file that is not there -- and on a
	// case-insensitive filesystem it would work locally and fail on the machine
	// the files sync to.
	world := newWorld(t)
	world.write(".mackup/myapp.cfg", definition("My App", []string{".Xresources", "Library/Preferences/My.plist"}, nil))

	files, _ := world.assemble("").Files("myapp")
	if want := []string{".Xresources", "Library/Preferences/My.plist"}; !reflect.DeepEqual(files, want) {
		t.Errorf("Files(\"myapp\") = %v, want %v with their case preserved", files, want)
	}
}

func TestXDGEntriesAreStoredHomeRelative(t *testing.T) {
	// appspec/05's worked example, structure and all: "-> key git, display name
	// Git, file set {.gitconfig, .config/git/config, .config/git/ignore} (with
	// default XDG base ~/.config)". The two sections are one file set, and a
	// consumer "cannot tell an XDG-sourced path from a plain one".
	world := newWorld(t)
	world.write(".mackup/mygit.cfg", definition("My Git", []string{".gitconfig"}, []string{"git/config", "git/ignore"}))

	files, _ := world.assemble("").Files("mygit")
	want := []string{".config/git/config", ".config/git/ignore", ".gitconfig"}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("Files(\"mygit\") = %v, want %v", files, want)
	}
}

func TestTheSameSpellingInBothSectionsIsTwoDifferentFiles(t *testing.T) {
	// "git/config" as an XDG entry is ~/.config/git/config; as a plain entry it
	// is ~/git/config. Several shipped definitions carry exactly that pair, so a
	// union taken before the XDG half is relativized silently drops one of the
	// two files an application syncs.
	world := newWorld(t)
	world.write(".mackup/myapp.cfg", definition("My App", []string{"git/config"}, []string{"git/config"}))

	files, _ := world.assemble("").Files("myapp")
	if want := []string{".config/git/config", "git/config"}; !reflect.DeepEqual(files, want) {
		t.Errorf("Files(\"myapp\") = %v, want both files %v", files, want)
	}
}

func TestARepeatedPathAppearsOnceInTheFileSet(t *testing.T) {
	// The file set is a set. The same path written in both sections after
	// relativization is one file, and appspec/05 calls the result a union.
	world := newWorld(t)
	world.write(".mackup/myapp.cfg", definition("My App", []string{".config/git/config"}, []string{"git/config"}))

	files, _ := world.assemble("").Files("myapp")
	if want := []string{".config/git/config"}; !reflect.DeepEqual(files, want) {
		t.Errorf("Files(\"myapp\") = %v, want %v once", files, want)
	}
}

func TestADefinitionWithNeitherPathSectionIsStillAnApplication(t *testing.T) {
	// appspec/05: "A definition with neither contributes an application that has
	// an empty file set (it still appears in list and show)." 354 of the 614
	// shipped definitions are that shape, so a reader that skipped them would
	// lose more than half the catalog.
	world := newWorld(t)
	world.write(".mackup/myapp.cfg", definition("My App", nil, nil))

	files, ok := world.assemble("").Files("myapp")
	if !ok {
		t.Fatal("a definition with no path sections did not produce an application")
	}
	if len(files) != 0 {
		t.Errorf("Files(\"myapp\") = %v, want an empty file set", files)
	}
}

func TestAnUnknownKeyIsDistinguishableFromAnEmptyFileSet(t *testing.T) {
	// The reason both lookups report a second result: appspec/05 makes an empty
	// file set valid and makes an unknown key a fatal "Unsupported application:
	// <key>", so a caller that read emptiness as absence would refuse an
	// application the database holds.
	world := newWorld(t)
	world.write(".mackup/myapp.cfg", definition("My App", nil, nil))
	db := world.assemble("")

	if _, ok := db.Files("myapp"); !ok {
		t.Error("a known application with an empty file set reported as unknown")
	}
	if name, ok := db.Name("frobnicate"); ok || name != "" {
		t.Errorf("Name(\"frobnicate\") = (%q, %v), want it unknown", name, ok)
	}
	if files, ok := db.Files("frobnicate"); ok || files != nil {
		t.Errorf("Files(\"frobnicate\") = (%v, %v), want it unknown", files, ok)
	}
}

func TestAnEmptyNameValueIsADisplayNameAndNotAMissingOne(t *testing.T) {
	// The distinction ini.Section.Value reports, applied here: `name =` is a
	// definition that names its application the empty string, which appspec/05
	// does not forbid. Collapsing it into "absent" would abort a run over a file
	// that has the required key.
	world := newWorld(t)
	world.write(".mackup/myapp.cfg", "[application]\nname =\n")

	if name, ok := world.assemble("").Name("myapp"); !ok || name != "" {
		t.Errorf("Name(\"myapp\") = (%q, %v), want an empty display name on a known key", name, ok)
	}
}

func TestADefinitionWithNoNameIsRefused(t *testing.T) {
	// appspec/05 makes the [application] section and its name key "required",
	// and lists that among the rules that are "each part of the contract".
	// appspec/07's table gives the condition no row, so it takes the unguarded
	// shape its neighbours in that column take and names the file.
	for _, c := range []struct{ what, content string }{
		{"no [application] section", "[configuration_files]\n.myapprc\n"},
		{"no name key", "[application]\n[configuration_files]\n.myapprc\n"},
		{"a differently-cased name key", "[application]\nName = My App\n"},
	} {
		world := newWorld(t)
		world.write(".mackup/myapp.cfg", c.content)

		err := world.refuse("")
		if !strings.Contains(err.Error(), "myapp.cfg") {
			t.Errorf("%s: the diagnostic %q does not name the definition file", c.what, err)
		}
		if regime, declared := fault.RegimeOf(err); !declared || regime != fault.Unguarded {
			t.Errorf("%s: the failure reads as (%v, declared=%v), want the unguarded regime", c.what, regime, declared)
		}
	}
}

func TestAnAbsolutePathIsRefusedInEitherSection(t *testing.T) {
	// appspec/05: "A path that starts with '/' (an absolute path) is rejected:
	// assembling the database fails with a fatal (uncaught) error naming the
	// offending path (Unsupported absolute path: <path>), nonzero exit", and the
	// same "exactly like an absolute [configuration_files] path" for an XDG
	// entry. appspec/07 puts both in the unguarded column.
	//
	// This is the rejection appspec/05 calls "load-bearing for the sync engine's
	// safety": appspec/06 never re-checks that a path is home-relative.
	for _, c := range []struct{ what, content string }{
		{"[configuration_files]", definition("My App", []string{"/etc/passwd"}, nil)},
		{"[xdg_configuration_files]", definition("My App", nil, []string{"/etc/passwd"})},
	} {
		world := newWorld(t)
		world.write(".mackup/myapp.cfg", c.content)

		err := world.refuse("")
		if want := "Unsupported absolute path: /etc/passwd"; !strings.Contains(err.Error(), want) {
			t.Errorf("%s: the diagnostic is %q, want %q inside it", c.what, err, want)
		}
		if regime, declared := fault.RegimeOf(err); !declared || regime != fault.Unguarded {
			t.Errorf("%s: the failure reads as (%v, declared=%v), want the unguarded regime", c.what, regime, declared)
		}
	}
}

func TestTheRefusedDefinitionIsTheSameOneOnEveryRun(t *testing.T) {
	// Two refusable definitions in one directory, and nothing in appspec/05
	// chooses between them -- so the choice is made by sorted key order rather
	// than by map iteration, and it is pinned here because a diagnostic that
	// alternated between two paths is a diagnostic nothing can be written
	// against.
	world := newWorld(t)
	world.write(".mackup/aaa.cfg", definition("A", []string{"/first"}, nil))
	world.write(".mackup/zzz.cfg", definition("Z", []string{"/second"}, nil))

	for range 8 {
		err := world.refuse("")
		if want := "Unsupported absolute path: /first"; !strings.Contains(err.Error(), want) {
			t.Fatalf("the diagnostic is %q, want %q: the first key in sorted order", err, want)
		}
	}
}

func TestAnXDGConfigHomeOutsideTheHomeDirectoryIsRefused(t *testing.T) {
	// appspec/05: "If $XDG_CONFIG_HOME resolves to a location not within the
	// home directory, database assembly fails with a fatal (uncaught) error
	// stating that $XDG_CONFIG_HOME must be somewhere within the home directory,
	// nonzero exit." appspec/07's row asks the diagnostic to name the value.
	//
	// The relative case is the one a containment check written against the raw
	// variable passes: "elsewhere" is not lexically outside anything, and it
	// resolves against the working directory the process happened to start in.
	world := newWorld(t)
	outside := t.TempDir()

	for _, c := range []struct{ what, value, mentions string }{
		{"an absolute path outside home", outside, outside},
		{"a relative path", "elsewhere", "elsewhere"},
	} {
		err := world.refuse(c.value)
		if !strings.Contains(err.Error(), "$XDG_CONFIG_HOME") {
			t.Errorf("%s: the diagnostic is %q, want it to name $XDG_CONFIG_HOME", c.what, err)
		}
		if !strings.Contains(err.Error(), c.mentions) {
			t.Errorf("%s: the diagnostic is %q, want %q inside it", c.what, err, c.mentions)
		}
		if regime, declared := fault.RegimeOf(err); !declared || regime != fault.Unguarded {
			t.Errorf("%s: the failure reads as (%v, declared=%v), want the unguarded regime", c.what, regime, declared)
		}
	}
}

func TestTheXDGBaseIsCheckedBeforeAnyDefinitionIsRead(t *testing.T) {
	// appspec/05 states the consequence unconditionally -- "this check fires
	// while assembling the database, so it blocks every command" -- which is
	// only true if it does not wait for a definition that happens to carry an
	// [xdg_configuration_files] section.
	//
	// Observed by making the OTHER rejection reachable: ~/.mackup holds a
	// definition with an absolute path, which every path through assembly would
	// otherwise refuse. The XDG diagnostic winning is evidence the base was
	// checked first, and it is the diagnostic a user must see -- the absolute
	// path is a consequence of nothing, while the XDG base is what they set.
	world := newWorld(t)
	world.write(".mackup/aaa.cfg", definition("A", []string{"/etc/passwd"}, nil))

	err := world.refuse(t.TempDir())
	if !strings.Contains(err.Error(), "$XDG_CONFIG_HOME") {
		t.Errorf("the diagnostic is %q, want the $XDG_CONFIG_HOME refusal to come first", err)
	}
}

func TestAnXDGConfigHomeInsideHomeMovesBothThingsItDecides(t *testing.T) {
	// $XDG_CONFIG_HOME decides two things at once, and a program that honoured
	// it for one would look conformant under most fixtures: where the XDG
	// custom-apps directory is (appspec/05 "Discovery" tier 2), and what every
	// [xdg_configuration_files] entry means (appspec/05 "the XDG base is joined
	// to p").
	world := newWorld(t)
	world.write("xdg/mackup/applications/myapp.cfg", definition("My App", nil, []string{"myapp/config"}))
	db := world.assemble(filepath.Join(world.home, "xdg"))

	if _, ok := db.Name("myapp"); !ok {
		t.Fatal("the XDG apps directory under $XDG_CONFIG_HOME was not read")
	}
	files, _ := db.Files("myapp")
	if want := []string{"xdg/myapp/config"}; !reflect.DeepEqual(files, want) {
		t.Errorf("Files(\"myapp\") = %v, want %v relative to the moved XDG base", files, want)
	}
}

func TestATildeInXDGConfigHomeIsExpanded(t *testing.T) {
	// The XDG base is resolved by internal/homepath, which appspec/03 already
	// requires of the same variable when it names a config candidate. The two
	// stages must agree about what "~/xdg" is, or the program reads its config
	// from one directory and its definitions from another.
	world := newWorld(t)
	world.write("xdg/mackup/applications/myapp.cfg", definition("My App", nil, []string{"myapp/config"}))
	db := world.assemble("~/xdg")

	if _, ok := db.Name("myapp"); !ok {
		t.Fatal("a tilde in $XDG_CONFIG_HOME was not expanded; the apps directory went unread")
	}
	files, _ := db.Files("myapp")
	if want := []string{"xdg/myapp/config"}; !reflect.DeepEqual(files, want) {
		t.Errorf("Files(\"myapp\") = %v, want %v", files, want)
	}
}

func TestADefinitionThatCannotBeReadIsRefusedRatherThanSkipped(t *testing.T) {
	// A file the discovery step took and the read step cannot open. Treating it
	// as empty is the one thing assembly must not do: for a new key that
	// silently drops an application the user's own directory claims, and for an
	// overriding one it resurrects the built-in definition the user wrote the
	// file to replace.
	if os.Geteuid() == 0 {
		t.Skip("running as root, which can read a file with no permission bits")
	}
	world := newWorld(t)
	path := world.write(".mackup/myapp.cfg", definition("My App", nil, nil))
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("sealing the definition file: %v", err)
	}

	err := world.refuse("")
	if !strings.Contains(err.Error(), "myapp.cfg") {
		t.Errorf("the diagnostic is %q, want it to name the definition file", err)
	}
}

func TestHomeMustBeSetAndAbsolute(t *testing.T) {
	// Unreachable from the pipeline -- config load is step 2 and refuses an
	// unset or relative HOME first -- and asserted anyway, because a package
	// whose correctness depends on the order its caller runs stages in has no
	// contract of its own. Both take the unguarded regime appspec/03 gives the
	// same condition.
	for _, home := range []string{"", "relative/home"} {
		db, err := Assemble(Environment{Home: home})
		if err == nil {
			t.Fatalf("HOME=%q assembled %d applications; want a refusal", home, len(db.Keys()))
		}
		if regime, declared := fault.RegimeOf(err); !declared || regime != fault.Unguarded {
			t.Errorf("HOME=%q failed as (%v, declared=%v), want the unguarded regime", home, regime, declared)
		}
	}
}

func TestTheDatabaseHandsOutCopies(t *testing.T) {
	// The database is immutable in the same sense config.Config is: appspec/01
	// section 7 re-assembles it mid-run rather than mutating it. A caller that
	// sorted or truncated a returned slice would otherwise rewrite what the next
	// caller sees -- and config.Scope is handed exactly the slice Keys returns.
	world := newWorld(t)
	world.write(".mackup/myapp.cfg", definition("My App", []string{"b", "a"}, nil))
	db := world.assemble("")

	keys := db.Keys()
	keys[0] = "mutated"
	if db.Keys()[0] == "mutated" {
		t.Error("writing to the slice Keys returned changed the database")
	}
	files, _ := db.Files("myapp")
	files[0] = "mutated"
	if again, _ := db.Files("myapp"); again[0] == "mutated" {
		t.Error("writing to the slice Files returned changed the database")
	}
}

func TestADefinitionIsReadInTheDialectAppspec05Describes(t *testing.T) {
	// appspec/05 calls a definition "one INI-style .cfg file" and appspec/03
	// describes that dialect. Each fixture writes a comment, an odd whitespace
	// arrangement or an unrecognized section around the same two facts; a reader
	// that mishandled one reports a different name or a different file set.
	for _, c := range []struct{ what, content string }{
		{"whole-line comments", "# a comment\n; another\n[application]\nname = My App\n[configuration_files]\n.myapprc\n"},
		{"an inline comment", "[application]\nname = My App ; a comment\n[configuration_files]\n.myapprc # another\n"},
		{"whitespace around everything", "  [application]  \n\tname   =   My App  \n  [configuration_files]  \n\t.myapprc  \n"},
		{"an unrecognized section", "[application]\nname = My App\n[whatever]\nkey = value\n[configuration_files]\n.myapprc\n"},
		{"blank lines throughout", "\n[application]\n\nname = My App\n\n[configuration_files]\n\n.myapprc\n\n"},
		{"a repeated section header", "[application]\nname = My App\n[configuration_files]\n.myapprc\n[configuration_files]\n.myapprc\n"},
	} {
		world := newWorld(t)
		world.write(".mackup/myapp.cfg", c.content)
		db := world.assemble("")

		if name, _ := db.Name("myapp"); name != "My App" {
			t.Errorf("%s: Name(\"myapp\") = %q, want %q", c.what, name, "My App")
		}
		files, _ := db.Files("myapp")
		if want := []string{".myapprc"}; !reflect.DeepEqual(files, want) {
			t.Errorf("%s: Files(\"myapp\") = %v, want %v", c.what, files, want)
		}
	}
}
