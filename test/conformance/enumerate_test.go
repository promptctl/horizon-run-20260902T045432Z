//go:build conformance

// The enumeration half of the suite: `list` and `show` as
// appspec/05-application-database.md "Enumeration" specifies them, observed at
// the program's boundary.
//
// These are the first two commands whose SUCCESS is observable. Every case
// written before them could only watch the program refuse -- which is why the
// config and application-database files each opened with a note about what
// their stage did not yet print. What those notes deferred lands here: the
// contents of the assembled database (keys, display names, file sets), the
// count trailer, and the storage root, all read off stdout and stderr rather
// than out of a package's own tests.
//
// appspec/00 promise 5 is what makes this more than formatting: "list and show
// let the user see the whole catalog and any one application's exact file set
// before running anything". A listing that quietly omitted an application, or
// a show that folded a path's case, would break the audit the rest of the
// program's safety argument is built on.

package conformance

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The literal shapes appspec/05 "Enumeration" gives the two commands, and the
// one literal token appspec/07 gives their failure.
const (
	listHeader                   = "Supported applications:"
	showNamePrefix               = "Name: "
	showFilesHeader              = "Configuration files:"
	entryPrefix                  = " - "
	unsupportedApplicationPrefix = "Unsupported application: "
)

// catalogSize is the number of applications the built-in catalog ships, which
// appspec/05 fixes for the reference build ("Reference build: N = 614") and
// appspec/appendix-application-names.md records key by key.
//
// Asserted at the boundary as a NUMBER rather than as "however many the binary
// happens to hold", which is the whole difference between a conformance claim
// and a tautology: internal/catalog pins the shipped files against the
// appendix, and this pins what the program prints against the appendix's own
// total. A definition file deleted or renamed moves this and nothing else.
const catalogSize = 614

// storageRoot is the world-relative storage directory UseResolvableStorage
// creates, named here so a case can talk about the same directory the helper
// made.
const storageRoot = "storage"

// aListing is `list` output taken apart into the pieces appspec/05 gives it:
// the keys between the header and the blank line, and the two values in the
// count trailer.
type aListing struct {
	keys    []string
	count   int
	version string
}

// trailerPattern matches appspec/05's count trailer, "<N> applications
// supported in Mackup v<version>", anchored so a program that printed
// something else on that line cannot satisfy it by containing it.
var trailerPattern = regexp.MustCompile(`^(\d+) applications supported in Mackup v(\S+)$`)

// parseListing reads a `list` result as appspec/05 describes the block, and
// fails the case if the shape is not that block.
//
// The shape is checked before anything is read out of it, so a case asserting
// something ABOUT the keys cannot pass over output that is not a listing --
// the failure a Contains on a 617-line stream would wave through. It is the
// same lesson the version banner taught this suite: three cases asserted
// ExpectStdout("Mackup ") and were satisfied by the usage block.
func parseListing(t *testing.T, r Result) aListing {
	t.Helper()
	r.ExpectExit(0)
	if r.Stderr != "" {
		t.Errorf("%s wrote %q to stderr; appspec/07 puts list output on stdout", r.invocation(), r.Stderr)
	}
	lines := strings.Split(strings.TrimSuffix(r.StdoutText(), "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("%s stdout = %q, want the header, key lines, a blank line and the trailer", r.invocation(), r.Stdout)
	}
	if lines[0] != listHeader {
		t.Fatalf("%s first line = %q, want %q", r.invocation(), lines[0], listHeader)
	}
	if blank := lines[len(lines)-2]; blank != "" {
		t.Errorf("%s: the line before the trailer = %q, want it blank as appspec/05 writes the block", r.invocation(), blank)
	}
	listing := aListing{}
	for _, line := range lines[1 : len(lines)-2] {
		if !strings.HasPrefix(line, entryPrefix) {
			t.Fatalf("%s key line = %q, want it to open with %q", r.invocation(), line, entryPrefix)
		}
		listing.keys = append(listing.keys, strings.TrimPrefix(line, entryPrefix))
	}
	trailer := trailerPattern.FindStringSubmatch(lines[len(lines)-1])
	if trailer == nil {
		t.Fatalf("%s trailer = %q, want %q", r.invocation(), lines[len(lines)-1],
			"<N> applications supported in Mackup v<version>")
	}
	listing.count, _ = strconv.Atoi(trailer[1])
	listing.version = trailer[2]

	// The two halves of appspec/05's own observed effect -- a dropped
	// definition "makes key myapp appear in list, INCREMENTS the
	// supported-count trailer by one" -- are one claim, so every case that
	// reads a listing gets them checked against each other for free.
	if listing.count != len(listing.keys) {
		t.Errorf("%s: the trailer counts %d applications and %d were printed", r.invocation(), listing.count, len(listing.keys))
	}
	if !sort.StringsAreSorted(listing.keys) {
		t.Errorf("%s: the keys are not sorted ascending, which appspec/05 requires", r.invocation())
	}
	return listing
}

// listedKeys is parseListing's answer as a set, for a case that asks whether a
// key is there rather than what the whole listing is.
func listedKeys(t *testing.T, r Result) map[string]bool {
	t.Helper()
	set := map[string]bool{}
	for _, key := range parseListing(t, r).keys {
		set[key] = true
	}
	return set
}

// expectListed asserts every named key appears in a listing.
func expectListed(t *testing.T, r Result, keys ...string) {
	t.Helper()
	listed := listedKeys(t, r)
	for _, key := range keys {
		if !listed[key] {
			t.Errorf("%s did not print the application %q", r.invocation(), key)
		}
	}
}

// showLines returns the file paths a successful `show` printed, after checking
// the two labels appspec/05 puts above them.
func showLines(t *testing.T, r Result) (name string, paths []string) {
	t.Helper()
	r.ExpectExit(0)
	if r.Stderr != "" {
		t.Errorf("%s wrote %q to stderr; appspec/07 puts show output on stdout", r.invocation(), r.Stderr)
	}
	lines := strings.Split(strings.TrimSuffix(r.StdoutText(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("%s stdout = %q, want a name line and the configuration-files header", r.invocation(), r.Stdout)
	}
	if !strings.HasPrefix(lines[0], showNamePrefix) {
		t.Fatalf("%s first line = %q, want it to open with %q", r.invocation(), lines[0], showNamePrefix)
	}
	if lines[1] != showFilesHeader {
		t.Fatalf("%s second line = %q, want %q", r.invocation(), lines[1], showFilesHeader)
	}
	for _, line := range lines[2:] {
		if !strings.HasPrefix(line, entryPrefix) {
			t.Fatalf("%s path line = %q, want it to open with %q", r.invocation(), line, entryPrefix)
		}
		paths = append(paths, strings.TrimPrefix(line, entryPrefix))
	}
	if !sort.StringsAreSorted(paths) {
		t.Errorf("%s printed %v, want the file set sorted ascending", r.invocation(), paths)
	}
	return strings.TrimPrefix(lines[0], showNamePrefix), paths
}

func TestListPrintsTheWholeShippedCatalog(t *testing.T) {
	// The epic's own done-claim, read at the boundary: "list and show conform
	// to appspec/05 Enumeration with the shipped 614-key catalog".
	//
	// The count is the appendix's total, not the binary's own idea of it.
	// internal/catalog checks the shipped files against
	// appspec/appendix-application-names.md; this checks what the PROGRAM
	// prints against the same number, so a definition lost between the
	// directory and the listing has somewhere to be caught.
	world := NewWorld(t)
	world.UseResolvableStorage()

	listing := parseListing(t, world.Run("list"))
	if listing.count != catalogSize {
		t.Errorf("list reported %d applications, want the appendix's %d", listing.count, catalogSize)
	}
	// Named keys as well as a count, because a count alone is satisfied by 614
	// of anything. These four are spread across the sorted range and each is
	// an application the rest of the specification refers to by name.
	expectListed(t, world.Run("list"), "vim", "git", "mackup", "zsh")
}

func TestShowPrintsMackupsOwnConfigurationFiles(t *testing.T) {
	// The one shipped definition whose CONTENT the specification fixes rather
	// than leaving to the catalog's authors: appspec/06 names it in passing --
	// "the application itself (key mackup, which manages .mackup.cfg and the
	// .mackup directory)" -- and appspec/03 and appspec/05 are what make those
	// two paths the user's own configuration and custom-apps directory.
	//
	// It is asserted at the boundary because whole-Mackup mode depends on it:
	// appspec/06's no-argument link path links the user's own Mackup config
	// FIRST and re-reads it, which is only possible if this definition covers
	// it. Every other definition's file set is authored data this project
	// chose; this one is a consequence of the specification, so it is checked
	// where the specification makes its promises.
	world := NewWorld(t)
	world.UseResolvableStorage()

	name, paths := showLines(t, world.Run("show", "mackup"))
	if name == "" {
		t.Error("show mackup printed an empty display name")
	}
	want := []string{".mackup", ".mackup.cfg"}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Errorf("show mackup printed %v, want %v", paths, want)
	}
}

func TestTheListTrailerCarriesTheProgramsOwnVersion(t *testing.T) {
	// appspec/05 writes the trailer as "Mackup v<version>", and appspec/00
	// "Provenance" makes that version a contract with a fallback token rather
	// than a constant. A trailer hardcoding the reference build's 0.11.1 would
	// satisfy the format on every machine and report a version this build is
	// not -- which is exactly the vacuity ExpectVersionLine was written for on
	// the banner, arriving on the other line that carries a version.
	//
	// The release build is the one that makes this observable: it is stamped
	// with a version nothing else in the tree spells, so the trailer can only
	// carry it by asking the same source --version asks.
	world := NewWorld(t)
	world.UseStampedBinary()
	world.UseResolvableStorage()

	listing := parseListing(t, world.Run("list"))
	if listing.version != stampedVersion {
		t.Errorf("the trailer reports version %q, want the stamped %q", listing.version, stampedVersion)
	}
	world.Run("--version").ExpectStdoutLine("Mackup " + stampedVersion)
}

func TestADroppedDefinitionAppearsInListAndIncrementsTheCount(t *testing.T) {
	// appspec/05 "Observed effects of adding a custom definition": dropping
	// ~/.mackup/myapp.cfg "makes key myapp appear in list, increments the
	// supported-count trailer by one, makes show myapp print its display name
	// and file paths".
	//
	// All three, in one world, because they are one claim about one file.
	world := NewWorld(t)
	world.UseResolvableStorage()
	world.WriteFile(".mackup/myapp.cfg",
		"[application]\nname = My App\n\n[configuration_files]\n.myapprc\n", 0o600)

	listing := parseListing(t, world.Run("list"))
	if listing.count != catalogSize+1 {
		t.Errorf("list reported %d applications, want %d: one more than the built-in set", listing.count, catalogSize+1)
	}
	expectListed(t, world.Run("list"), "myapp")

	name, paths := showLines(t, world.Run("show", "myapp"))
	if name != "My App" {
		t.Errorf("show myapp printed the name %q, want %q", name, "My App")
	}
	if len(paths) != 1 || paths[0] != ".myapprc" {
		t.Errorf("show myapp printed %v, want [.myapprc]", paths)
	}
}

func TestADroppedDefinitionReplacesTheBuiltInOneEntirely(t *testing.T) {
	// appspec/05: dropping ~/.mackup/vim.cfg "REPLACES the built-in vim
	// definition entirely (the built-in vim.cfg is not read at all for that
	// key)".
	//
	// Replacement and not merger: the built-in definition carries .vimrc and
	// .vim, and neither may survive. A program that layered the two file sets
	// would still print one vim key and one count, so only the paths tell the
	// two behaviours apart.
	world := NewWorld(t)
	world.UseResolvableStorage()
	world.WriteFile(".mackup/vim.cfg",
		"[application]\nname = My Vim\n\n[configuration_files]\n.my-vim-only\n", 0o600)

	if listing := parseListing(t, world.Run("list")); listing.count != catalogSize {
		t.Errorf("list reported %d applications, want %d: an override adds no key", listing.count, catalogSize)
	}
	name, paths := showLines(t, world.Run("show", "vim"))
	if name != "My Vim" {
		t.Errorf("show vim printed the name %q, want the override's %q", name, "My Vim")
	}
	if len(paths) != 1 || paths[0] != ".my-vim-only" {
		t.Errorf("show vim printed %v, want only the override's [.my-vim-only]", paths)
	}
}

func TestShowPrintsFilePathsSortedAndWithTheirExactCase(t *testing.T) {
	// appspec/05 makes both properties contract, and they are the half of
	// appspec/03's case-policy pair that this file owns: "Key names here are
	// case-preserving (they are not lowercased, unlike the user-config
	// application lists in 03) so paths keep their exact case."
	//
	// It matters beyond tidiness, and appspec/05 says why: the sync engine
	// joins these paths to HOME without re-checking them. A folded path
	// resolves on a case-insensitive filesystem and silently misses its file
	// on a case-sensitive one -- the failure that works on the machine it was
	// written on.
	//
	// The definition lists its paths OUT of order, so the sort is the
	// program's rather than the file's.
	world := NewWorld(t)
	world.UseResolvableStorage()
	world.WriteFile(".mackup/cased.cfg",
		"[application]\nname = Cased\n\n[configuration_files]\n.zshrc\n.Xresources\nLibrary/Preferences/Some.plist\n", 0o600)

	_, paths := showLines(t, world.Run("show", "cased"))
	want := []string{".Xresources", ".zshrc", "Library/Preferences/Some.plist"}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Errorf("show cased printed %v, want %v", paths, want)
	}
}

func TestShowPrintsXDGEntriesAsHomeRelativePaths(t *testing.T) {
	// appspec/05: for each [xdg_configuration_files] path p, "the XDG base is
	// joined to p, then the home prefix is stripped so the item is
	// stored/looked-up relative to home just like a [configuration_files]
	// entry", with $XDG_CONFIG_HOME defaulting to ~/.config.
	//
	// Both the default and a moved base, because a program that hardcoded
	// ".config/" would print the right thing under the default and the wrong
	// thing under the variable -- and appspec/05 makes the variable the base
	// of the relativization, not just of the directory it reads.
	definition := "[application]\nname = Xdg App\n\n[xdg_configuration_files]\nxdgapp/config\n"

	byDefault := NewWorld(t)
	byDefault.UseResolvableStorage()
	byDefault.WriteFile(".mackup/xdgapp.cfg", definition, 0o600)
	if _, paths := showLines(t, byDefault.Run("show", "xdgapp")); len(paths) != 1 || paths[0] != ".config/xdgapp/config" {
		t.Errorf("show xdgapp printed %v, want [.config/xdgapp/config]", paths)
	}

	moved := NewWorld(t)
	moved.UseResolvableStorage()
	moved.Setenv("XDG_CONFIG_HOME", moved.Path("elsewhere"))
	moved.WriteFile(".mackup/xdgapp.cfg", definition, 0o600)
	if _, paths := showLines(t, moved.Run("show", "xdgapp")); len(paths) != 1 || paths[0] != "elsewhere/xdgapp/config" {
		t.Errorf("show xdgapp printed %v, want [elsewhere/xdgapp/config]", paths)
	}
}

func TestShowMergesTheTwoPathSectionsIntoOneFileSet(t *testing.T) {
	// appspec/05: "The final file set for an application is the UNION of its
	// [configuration_files] entries and its (home-relativized)
	// [xdg_configuration_files] entries. The two sections are two authoring
	// sources for ONE file set; a consumer cannot tell an XDG-sourced path
	// from a plain one."
	//
	// The same spelling in both sections is the case that makes the union a
	// union rather than a concatenation with a de-duplication bug: "app/conf"
	// under [configuration_files] is ~/app/conf, and under
	// [xdg_configuration_files] it is ~/.config/app/conf. Several shipped
	// definitions carry exactly that pair, so collapsing them by the written
	// spelling would drop a real file.
	world := NewWorld(t)
	world.UseResolvableStorage()
	world.WriteFile(".mackup/both.cfg",
		"[application]\nname = Both\n\n[configuration_files]\nboth/conf\n\n[xdg_configuration_files]\nboth/conf\n", 0o600)

	_, paths := showLines(t, world.Run("show", "both"))
	want := []string{".config/both/conf", "both/conf"}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Errorf("show both printed %v, want %v: the same spelling in the two sections is two different files", paths, want)
	}
}

func TestAnApplicationWithNoFilesStillListsAndShows(t *testing.T) {
	// appspec/05: "A definition may have [configuration_files] only,
	// [xdg_configuration_files] only, both, or neither. A definition with
	// neither contributes an application that has an empty file set (IT STILL
	// APPEARS IN LIST AND SHOW)."
	//
	// So the "Configuration files:" label is printed with nothing under it.
	// Dropping the label there would make an application whose paths are not
	// yet authored -- which the shipped catalog has, deliberately -- look like
	// one the program failed to read.
	world := NewWorld(t)
	world.UseResolvableStorage()
	world.WriteFile(".mackup/bare.cfg", "[application]\nname = Bare\n", 0o600)

	expectListed(t, world.Run("list"), "bare")
	name, paths := showLines(t, world.Run("show", "bare"))
	if name != "Bare" || len(paths) != 0 {
		t.Errorf("show bare printed name %q and paths %v, want %q and none", name, paths, "Bare")
	}
}

func TestTheApplicationKeyIsTheFilenameAndKeepsItsCase(t *testing.T) {
	// appspec/05: "the file's basename without the .cfg extension IS the
	// application key". Not a lowercased basename -- appspec/03 normalizes the
	// CONFIG's application lists and says so as one half of a pair whose other
	// half is this.
	//
	// Every fixture in this suite but this one names a lowercase definition
	// file, and every key the shipped catalog holds is lowercase, so a program
	// that folded the key would pass all of them. That is the shape of hole
	// this ticket's predecessor found in the home rule: an arm no fixture
	// takes is an arm a green suite says nothing about.
	world := NewWorld(t)
	world.UseResolvableStorage()
	world.WriteFile(".mackup/MyApp.cfg", "[application]\nname = Mixed Case\n", 0o600)

	expectListed(t, world.Run("list"), "MyApp")
	if listedKeys(t, world.Run("list"))["myapp"] {
		t.Error("list printed the key lowercased; appspec/05 makes the basename the key")
	}
	if name, _ := showLines(t, world.Run("show", "MyApp")); name != "Mixed Case" {
		t.Errorf("show MyApp printed the name %q, want %q", name, "Mixed Case")
	}
	world.Run("show", "myapp").
		ExpectExit(1).
		ExpectStderrLine(unsupportedApplicationPrefix + "myapp").
		ExpectSilentStdout()
}

func TestShowRefusesAnUnknownApplicationWithTheLiteralToken(t *testing.T) {
	// appspec/05: "If <key> is not a known application, the program instead
	// writes Unsupported application: <key> and exits 1." appspec/07 lists the
	// prefix among the three literal tokens it calls contract, "matched by
	// scripts/tests".
	//
	// The whole line, once colour is stripped, and the token contiguous inside
	// the raw stream -- both are what ExpectStderrLine checks, and the second
	// is the half a script actually depends on.
	world := NewWorld(t)
	world.UseResolvableStorage()
	before := world.Snapshot()

	world.Run("show", "frobnicate").
		ExpectExit(1).
		ExpectStderrLine(unsupportedApplicationPrefix + "frobnicate").
		ExpectSilentStdout().
		ExpectStderrColor("91")
	world.ExpectUnchanged(before)
}

func TestListAndShowAreNarrowedByNothingInTheConfig(t *testing.T) {
	// appspec/03 of the allowlist, in as many words: "This section does NOT
	// affect list output." The denylist narrows "commands acting on all
	// applications", which is the SYNC fan-out of appspec/01 section 1, not
	// the catalog listing.
	//
	// This is the reading a reimplementation is most likely to get wrong,
	// because the selector is right there and applying it looks like the
	// consistent thing to do. It is also the reading that would break
	// appspec/00 promise 5: list and show exist so a user can "see the whole
	// catalog" -- including, and especially, the applications their config has
	// narrowed away.
	world := NewWorld(t)
	world.WriteFile(".mackup.cfg",
		"[storage]\nengine = file_system\npath = "+storageRoot+"\n"+
			"\n[applications_to_sync]\nvim\n"+
			"\n[applications_to_ignore]\ngit\nvim\n", 0o600)
	if err := os.MkdirAll(world.Path(storageRoot), 0o700); err != nil {
		t.Fatalf("creating the storage root: %v", err)
	}

	listing := parseListing(t, world.Run("list"))
	if listing.count != catalogSize {
		t.Errorf("list under an allowlist of one and a denylist of two reported %d applications, want all %d", listing.count, catalogSize)
	}
	expectListed(t, world.Run("list"), "vim", "git", "zsh")

	// show likewise: an ignored application is still one the user can audit.
	if name, _ := showLines(t, world.Run("show", "git")); name == "" {
		t.Error("show git printed an empty display name for an ignored application")
	}
}

func TestListAndShowFailIdenticallyWhenTheStorageFolderIsUnfindable(t *testing.T) {
	// This ticket's done-claim, and appspec/02's observed fact: "a run whose
	// configured (or default) engine cannot locate its storage folder fails at
	// load time with a fatal error and a non-zero exit REGARDLESS of which
	// subcommand was requested -- including list and show, which otherwise
	// touch no storage".
	//
	// Read here through the environment gate rather than the engine, which is
	// the other stage that can refuse a storage location: the config resolves,
	// and the directory it names is not there. Compared against backup's own
	// output rather than against a sentence written here, so the claim is
	// "identically" and not merely "also".
	world := NewWorld(t)
	world.WriteFile(".mackup.cfg", "[storage]\nengine = file_system\npath = "+storageRoot+"\n", 0o600)
	before := world.Snapshot()

	backup := world.Run("backup").ExpectExit(1).ExpectSilentStdout()
	if want := storageFolderRefusal + world.Path(storageRoot); backup.StderrText() != want+"\n" {
		t.Fatalf("mackup backup stderr = %q, want %q", backup.Stderr, want)
	}
	for _, argv := range [][]string{{"list"}, {"show", "vim"}, {"show", "frobnicate"}} {
		result := world.Run(argv...).ExpectExit(1).ExpectSilentStdout()
		if result.Stderr != backup.Stderr {
			t.Errorf("mackup %s stderr = %q, want the same diagnostic backup wrote: %q",
				strings.Join(argv, " "), result.Stderr, backup.Stderr)
		}
	}
	// `show frobnicate` above is the ordering claim: appspec/02 validates a
	// named application before the environment check only for the five sync
	// commands, "so that an unknown application fails cleanly before any
	// folder is created or any prompt is shown". show creates nothing and
	// prompts for nothing, so its gate comes first and the unknown key is
	// never reached.
	world.ExpectUnchanged(before)
}

func TestTheStorageRootMustBeADirectoryNotAFile(t *testing.T) {
	// appspec/01 section 4 level 1 is "storage-root DIRECTORY exists". A
	// regular file at that path cannot hold the Mackup folder appspec/06
	// creates inside it, so accepting one only moves the failure to the first
	// copy, where appspec/07's table has no row for it.
	world := NewWorld(t)
	world.WriteFile(".mackup.cfg", "[storage]\nengine = file_system\npath = "+storageRoot+"\n", 0o600)
	world.WriteFile(storageRoot, "not a directory", 0o600)

	world.Run("list").
		ExpectExit(1).
		ExpectStderrLine(storageFolderRefusal + world.Path(storageRoot)).
		ExpectSilentStdout()
}

func TestASymlinkedStorageRootIsFound(t *testing.T) {
	// The other side of the same check, and the reason it follows symlinks:
	// pointing ~/Dropbox at another volume with a symlink is ordinary, and the
	// directory it resolves to is the one the Mackup folder is created in.
	world := NewWorld(t)
	world.UseResolvableStorage()
	real := filepath.Join(world.Root, "elsewhere")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatalf("creating the real directory: %v", err)
	}
	if err := os.RemoveAll(world.Path(storageRoot)); err != nil {
		t.Fatalf("removing the storage root: %v", err)
	}
	if err := os.Symlink(real, world.Path(storageRoot)); err != nil {
		t.Fatalf("linking the storage root: %v", err)
	}

	world.Run("list").ExpectExit(0).ExpectStdout(listHeader)
}

func TestTheRootFlagHasNoEffectForAnOrdinaryUser(t *testing.T) {
	// appspec/07's root guard: "Running as a normal (non-root) user always
	// passes the guard; --root then has no observable effect." That file marks
	// the REFUSING arm UNVERIFIED for the reason this suite cannot fix -- the
	// harness runs as an ordinary user -- so what is observable here is the
	// permitted arm, and that the flag changes nothing when it is not needed.
	//
	// The refusing arm is driven in internal/app, through a seam, and that
	// division is deliberate: a guard whose refusal no test can reach is a
	// guard a green gate says nothing about.
	world := NewWorld(t)
	world.UseResolvableStorage()

	plain := world.Run("list").ExpectExit(0)
	for _, argv := range [][]string{{"--root", "list"}, {"-r", "list"}} {
		rooted := world.Run(argv...).ExpectExit(0)
		if rooted.Stdout != plain.Stdout {
			t.Errorf("mackup %s printed different output from `mackup list`", strings.Join(argv, " "))
		}
	}
}

func TestListAndShowOutputIsYellowOnStdoutOffATerminal(t *testing.T) {
	// appspec/07: "All human-facing output is ANSI-colored by level", normal
	// progress / info is yellow (33), and "the program does not condition
	// color on whether stdout is a TTY (observed: colors are emitted even when
	// output is piped/redirected)". These streams are pipes.
	//
	// The reset half is the one that costs something to get right: appspec/07
	// promises "every colored string is terminated with a reset", and list
	// output is the first multi-line block the program writes to stdout, so a
	// program colouring the block as one string would leave 615 lines opening
	// a colour they never close.
	world := NewWorld(t)
	world.UseResolvableStorage()

	world.Run("list").ExpectExit(0).ExpectStdoutColor("33").ExpectSilentStderr()
	world.Run("show", "vim").ExpectExit(0).ExpectStdoutColor("33").ExpectSilentStderr()
}

func TestListAndShowChangeNothingOnDisk(t *testing.T) {
	// appspec/01 section 1: list and show "otherwise touch no storage". They
	// are the program's audit surface (appspec/00 promise 5), and an audit
	// that wrote would not be one -- so this is asserted rather than assumed,
	// over the whole scratch root and not only the home directory, since the
	// storage folder can sit outside home.
	world := NewWorld(t)
	world.UseResolvableStorage()
	before := world.Snapshot()

	world.Run("list").ExpectExit(0)
	world.Run("show", "vim").ExpectExit(0)
	world.Run("show", "frobnicate").ExpectExit(1)

	world.ExpectUnchanged(before)
}
