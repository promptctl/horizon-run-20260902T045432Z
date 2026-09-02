//go:build conformance

// The config and storage-engine half of the suite: appspec/03-configuration.md
// and appspec/04-storage-engines.md, observed at the program's boundary.
//
// Two channels carry the cases here, and both are worth reading once rather
// than at each use.
//
// The first is WHICH config file the program decided to obey, and appspec/02's
// own error table supplies it: an unknown [storage] engine is an unguarded
// failure whose diagnostic must NAME THE OFFENDING VALUE. So a candidate
// config carrying `engine = from-xdg` reports its own name when it is the one
// that was read, and discovery precedence is directly observable without
// inventing any output the specification does not describe.
//
// The second is the VALUE of the resolved storage root, which the environment
// gate of macklebox-resolvers-5iw.4 made observable: appspec/01 section 4
// requires the storage-root directory to exist, and appspec/07 gives the
// failure the line `Error: Unable to find the storage folder: <path>`. A world
// whose engine resolves to a directory that is not there therefore reports the
// path the engine computed -- so "the Dropbox host database decodes to the
// path it encodes" and "a relative file_system path lands under home" are now
// black-box facts rather than internal/storage's own. The cases below use
// whichever channel their claim needs; several use both.

package conformance

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gatedCommands are the subcommands appspec/02 puts behind the universal
// config gate: every usage line except --help and --version. list and show
// are in the list on purpose -- appspec/01 section 1 says they "otherwise
// touch no storage", and that they die here anyway is the contract.
var gatedCommands = [][]string{
	{"list"},
	{"show", "vim"},
	{"backup"},
	{"restore"},
	{"link"},
	{"link", "install"},
	{"link", "uninstall"},
}

// storageFolderRefusal opens appspec/07's "Storage-root directory missing
// (usable-env check)" row: `Error: Unable to find the storage folder: <path>`.
//
// It is the line that made the resolved storage root observable at all -- see
// this file's header -- so several cases below read the path a storage engine
// computed off the end of it.
const storageFolderRefusal = "Error: Unable to find the storage folder: "

// driveFallbackRoot is the storage root recorded in internal/storage's
// other_root.db fixture, which that package's own tests name too. The fixture
// exists so a case can tell WHICH of the two Google Drive databases was read;
// the path is absolute, outside any world, and never created, so the
// environment gate names it back.
const driveFallbackRoot = "/Users/someone/Fallback Drive"

// writeConfig writes the world's ~/.mackup.cfg.
func writeConfig(w *World, content string) {
	w.t.Helper()
	w.WriteFile(".mackup.cfg", content, 0o600)
}

// unknownEngine is a config naming an engine that does not exist, whose value
// the diagnostic must repeat back. Used wherever a case needs to know WHICH
// config file the program read; see this file's header.
func unknownEngine(marker string) string {
	return "[storage]\nengine = " + marker + "\n"
}

func TestAConfigFailureAbortsEveryCommandIncludingListAndShow(t *testing.T) {
	// appspec/02 "Command dispatch order and the universal config-load gate",
	// stated as an observation: "with the default Dropbox engine and no
	// Dropbox install present, mackup list, mackup show vim, and mackup backup
	// <anything> all fail identically with the 'unable to find your Dropbox
	// install' fatal error and exit 1."
	//
	// Identically is asserted literally -- the streams are compared to the
	// first command's, not merely checked for a substring -- because the
	// failure this guards against is a gate that runs for the sync commands
	// and is skipped, or reworded, for the two that touch no storage.
	world := NewWorld(t)
	before := world.Snapshot()

	first := world.Run(gatedCommands[0]...).
		ExpectExit(1).
		ExpectSilentStdout().
		ExpectStderr("Unable to find your Dropbox install =(")

	for _, argv := range gatedCommands[1:] {
		result := world.Run(argv...).ExpectExit(1).ExpectSilentStdout()
		if result.Stderr != first.Stderr {
			t.Errorf("mackup %s wrote %q to stderr, want the same diagnostic mackup %s wrote: %q",
				strings.Join(argv, " "), result.Stderr, strings.Join(first.Args, " "), first.Stderr)
		}
	}

	// The post-condition both regimes of appspec/01 section 6 share: "no
	// stdout, no filesystem change, non-zero exit". Seven failed runs later,
	// the world is untouched.
	world.ExpectUnchanged(before)
}

func TestHelpAndVersionAreTheOnlyPathsPastTheConfigGate(t *testing.T) {
	// appspec/02: "--help and --version are the only paths that both succeed
	// (0) and skip the config-load gate." The world has no Dropbox install, so
	// anything that read the config would fail; these two must not notice.
	world := NewWorld(t)
	world.Run("--help").ExpectExit(0).ExpectStdout(usageMarker).ExpectSilentStderr()
	world.Run("--version").ExpectExit(0).ExpectVersionLine().ExpectSilentStderr()

	// And a bare invocation, which appspec/02 treats as a usage display rather
	// than a subcommand, so it never reaches the gate either.
	world.Run().ExpectExit(0).ExpectStdout(usageMarker).ExpectSilentStderr()
}

func TestTheHomeConfigOutranksBothEnvironmentCandidates(t *testing.T) {
	// appspec/03's first observed precedence fact: "with a ~/.mackup.cfg
	// present, it is used even when MACKUP_CONFIG and XDG_CONFIG_HOME both
	// point at other existing config files."
	world := NewWorld(t)
	writeConfig(world, unknownEngine("from-home"))
	world.WriteFile("named.cfg", unknownEngine("from-mackup-config"), 0o600)
	world.WriteFile("xdg/mackup/mackup.cfg", unknownEngine("from-xdg"), 0o600)
	world.Setenv("MACKUP_CONFIG", world.Path("named.cfg"))
	world.Setenv("XDG_CONFIG_HOME", world.Path("xdg"))

	expectConfigRead(t, world, "from-home")
}

func TestMackupConfigOutranksTheXDGCandidate(t *testing.T) {
	// appspec/03's second: "with no ~/.mackup.cfg, XDG_CONFIG_HOME's file is
	// used if present; if MACKUP_CONFIG also names an existing file,
	// MACKUP_CONFIG wins over the XDG candidate (it is checked earlier in the
	// list)."
	world := NewWorld(t)
	world.WriteFile("named.cfg", unknownEngine("from-mackup-config"), 0o600)
	world.WriteFile("xdg/mackup/mackup.cfg", unknownEngine("from-xdg"), 0o600)
	world.Setenv("MACKUP_CONFIG", world.Path("named.cfg"))
	world.Setenv("XDG_CONFIG_HOME", world.Path("xdg"))

	expectConfigRead(t, world, "from-mackup-config")
}

func TestTheXDGCandidateIsUsedWhenItIsTheOnlyOne(t *testing.T) {
	world := NewWorld(t)
	world.WriteFile("xdg/mackup/mackup.cfg", unknownEngine("from-xdg"), 0o600)
	world.Setenv("XDG_CONFIG_HOME", world.Path("xdg"))

	expectConfigRead(t, world, "from-xdg")
}

func TestTheXDGBaseDefaultsToDotConfigAndDropsTheLeadingDot(t *testing.T) {
	// appspec/03: with XDG_CONFIG_HOME unset the base is ~/.config, and the
	// filename there is "mackup.cfg" -- the specification calls the missing
	// dot out because it is the detail a reimplementation carries over from
	// ~/.mackup.cfg.
	world := NewWorld(t)
	world.WriteFile(".config/mackup/mackup.cfg", unknownEngine("from-default-xdg"), 0o600)
	expectConfigRead(t, world, "from-default-xdg")

	// The dotted spelling under the same directory is not a candidate, so a
	// world holding only that one falls through to the default engine.
	other := NewWorld(t)
	other.WriteFile(".config/mackup/.mackup.cfg", unknownEngine("wrong-name"), 0o600)
	other.Run("list").
		ExpectExit(1).
		ExpectStderr("Unable to find your Dropbox install =(").
		ExpectSilentStdout()
}

func TestAnEmptyMackupConfigVariableIsSkipped(t *testing.T) {
	// appspec/03: "If MACKUP_CONFIG is unset or empty, this candidate is
	// skipped." Empty rather than unset is the case worth running: expanded as
	// a path it is the working directory, which the harness sets to the
	// scratch root, so a program that did not skip it would look for a config
	// there.
	world := NewWorld(t)
	world.WriteFile("xdg/mackup/mackup.cfg", unknownEngine("from-xdg"), 0o600)
	world.Setenv("MACKUP_CONFIG", "")
	world.Setenv("XDG_CONFIG_HOME", world.Path("xdg"))

	expectConfigRead(t, world, "from-xdg")
}

func TestATildeInMackupConfigIsExpanded(t *testing.T) {
	world := NewWorld(t)
	world.WriteFile("named.cfg", unknownEngine("from-mackup-config"), 0o600)
	world.Setenv("MACKUP_CONFIG", "~/named.cfg")

	expectConfigRead(t, world, "from-mackup-config")
}

func TestACandidateThatIsNotARegularFileIsSkipped(t *testing.T) {
	// appspec/03 discovery takes "the first that exists AS A REGULAR FILE". A
	// directory named ~/.mackup.cfg is a plausible accident -- a botched
	// restore, a sync client resolving a conflict -- and a program that
	// stopped at it would fail trying to read a directory instead of moving on.
	world := NewWorld(t)
	if err := os.MkdirAll(world.Path(".mackup.cfg"), 0o700); err != nil {
		t.Fatalf("creating the fixture: %v", err)
	}
	world.WriteFile("xdg/mackup/mackup.cfg", unknownEngine("from-xdg"), 0o600)
	world.Setenv("XDG_CONFIG_HOME", world.Path("xdg"))

	expectConfigRead(t, world, "from-xdg")
}

// expectConfigRead asserts that the program obeyed the config file carrying
// this marker as its [storage] engine.
//
// The marker is not a valid engine, so the run fails; appspec/02 requires that
// failure's diagnostic to name the offending value, and the value is the
// marker. See this file's header for why the observation is made this way.
func expectConfigRead(t *testing.T, world *World, marker string) {
	t.Helper()
	before := world.Snapshot()
	result := world.Run("list").ExpectFailureExit().ExpectSilentStdout()
	if !strings.Contains(result.StderrText(), marker) {
		t.Errorf("mackup list wrote %q to stderr, want it to name %q -- the engine of the config file discovery should have chosen", result.Stderr, marker)
	}
	world.ExpectUnchanged(before)
}

func TestAnExplicitConfigSkipsDiscovery(t *testing.T) {
	// appspec/03 "Explicit override": "discovery is skipped and <path> is used
	// directly". The home config would otherwise win outright, so a program
	// that consulted discovery first reports "from-home" here.
	world := NewWorld(t)
	writeConfig(world, unknownEngine("from-home"))
	world.WriteFile("elsewhere.cfg", unknownEngine("from-c"), 0o600)

	result := world.Run("-c", world.Path("elsewhere.cfg"), "list").ExpectFailureExit().ExpectSilentStdout()
	if !strings.Contains(result.StderrText(), "from-c") {
		t.Errorf("stderr = %q, want the explicitly named config's engine", result.Stderr)
	}
}

func TestAnExplicitRelativePathResolvesAgainstHomeNotTheWorkingDirectory(t *testing.T) {
	// appspec/03: "a RELATIVE path is resolved relative to the home directory
	// (not the current working directory). So -c foo.cfg means ~/foo.cfg."
	//
	// A decoy of the same name is written in the working directory -- which
	// the harness sets to the scratch root, one level above home -- so a
	// program resolving against the cwd finds a file and reads the wrong
	// config rather than failing in a way that would be noticed.
	world := NewWorld(t)
	world.WriteFile("foo.cfg", unknownEngine("from-home"), 0o600)
	if err := os.WriteFile(filepath.Join(world.Root, "foo.cfg"), []byte(unknownEngine("from-cwd")), 0o600); err != nil {
		t.Fatalf("writing the decoy: %v", err)
	}

	result := world.Run("-c", "foo.cfg", "list").ExpectFailureExit().ExpectSilentStdout()
	if !strings.Contains(result.StderrText(), "from-home") {
		t.Errorf("stderr = %q, want ~/foo.cfg to have been read", result.Stderr)
	}
}

func TestAnExplicitTildePathIsExpanded(t *testing.T) {
	world := NewWorld(t)
	world.WriteFile("under-home.cfg", unknownEngine("from-tilde"), 0o600)

	result := world.Run("-c", "~/under-home.cfg", "list").ExpectFailureExit().ExpectSilentStdout()
	if !strings.Contains(result.StderrText(), "from-tilde") {
		t.Errorf("stderr = %q, want the tilde-expanded path to have been read", result.Stderr)
	}
}

func TestAnExplicitConfigThatDoesNotExistIsRefusedWithItsAbsolutePath(t *testing.T) {
	// appspec/07's table gives this row a literal shape, and appspec/03 says
	// the path in it is the absolute one -- which for a relative -c argument
	// is the home-relative resolution, not the two words the user typed.
	world := NewWorld(t)
	before := world.Snapshot()

	world.Run("-c", "nope.cfg", "list").
		ExpectExit(1).
		ExpectStderrLine("Error: The config file '" + world.Path("nope.cfg") + "' does not exist. Aborting.").
		ExpectSilentStdout()

	world.ExpectUnchanged(before)
}

func TestAConfigOutsideTheHomeDirectoryIsRefused(t *testing.T) {
	// appspec/03 "Home-directory containment", with the specification's own
	// example of a path outside home. The file EXISTS, which is what separates
	// this row of appspec/07's table from the one above: a program checking
	// containment before existence reports the wrong one.
	world := NewWorld(t)
	outside := filepath.Join(world.Root, "outside.cfg")
	if err := os.WriteFile(outside, []byte(unknownEngine("from-outside")), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	before := world.Snapshot()

	world.Run("-c", outside, "list").
		ExpectExit(1).
		ExpectStderrLine("Error: The config file '" + outside + "' is not in your home directory. Aborting.").
		ExpectSilentStdout()

	world.ExpectUnchanged(before)
}

func TestAHomeThatIsNotAnAbsolutePathAbortsEveryCommand(t *testing.T) {
	// appspec/03's environment table: HOME "must be set for the program to
	// function; if unset, home-relative operations fail with an uncaught error
	// (nonzero exit)" -- the unguarded regime, so the diagnostic names the
	// offending value rather than reading as one of the spec's sentences. A
	// relative value takes the same regime: it is not a home directory, and a
	// program that accepted one would resolve every path it later writes
	// against whatever directory it was started in.
	//
	// At the boundary because the rule has one implementation for two stages --
	// config discovery and database assembly both resolve home through it -- so
	// nothing but a run of the real program shows that the sentence a user sees
	// is the same one for both.
	for _, home := range []string{"", "relative/home"} {
		world := NewWorld(t)
		world.Setenv("HOME", home)
		before := world.Snapshot()

		result := world.Run("list").ExpectExit(1).ExpectSilentStdout()
		if home == "" {
			result.ExpectStderrLine("mackup: HOME is not set, so no home-relative path can be resolved")
		} else {
			result.ExpectStderrLine(`mackup: HOME is "relative/home", which is not an absolute path`)
		}

		world.ExpectUnchanged(before)
	}
}

func TestADiscoveredConfigOutsideTheHomeDirectoryIsRefusedToo(t *testing.T) {
	// appspec/03 makes the containment check independent of how the path was
	// arrived at: "checked at construction independently of whether the file
	// was discovered or explicitly named". MACKUP_CONFIG is the only candidate
	// that can point outside home, so it is the only way to observe that half
	// -- and a check written inside the -c branch passes every other case here.
	world := NewWorld(t)
	outside := filepath.Join(world.Root, "outside.cfg")
	if err := os.WriteFile(outside, []byte(unknownEngine("from-outside")), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	world.Setenv("MACKUP_CONFIG", outside)

	world.Run("list").
		ExpectExit(1).
		ExpectStderrLine("Error: The config file '" + outside + "' is not in your home directory. Aborting.").
		ExpectSilentStdout()
}

func TestALegacyConfigSectionRefusesToRunAnyCommand(t *testing.T) {
	// appspec/03 "Legacy config rejection" and appspec/07's "legacy config
	// sections present" row: a guarded, multi-line refusal at exit 1 that
	// "happens during config load, so it blocks every command".
	for _, section := range []string{"Allowed Applications", "Ignored Applications"} {
		world := NewWorld(t)
		writeConfig(world, "[storage]\nengine = file_system\npath = storage\n\n["+section+"]\nvim\n")
		before := world.Snapshot()

		for _, argv := range gatedCommands {
			result := world.Run(argv...).ExpectExit(1).ExpectSilentStdout()
			text := result.StderrText()
			if !strings.Contains(text, section) {
				t.Errorf("mackup %s wrote %q, want a refusal naming the legacy section [%s]", strings.Join(argv, " "), result.Stderr, section)
			}
			if strings.Count(strings.TrimSuffix(text, "\n"), "\n") < 1 {
				t.Errorf("mackup %s wrote %q, a single line; appspec/03 gives this a multi-line message", strings.Join(argv, " "), result.Stderr)
			}
		}
		world.ExpectUnchanged(before)
	}
}

func TestEachStorageEngineIsSelectedByItsConfigValue(t *testing.T) {
	// appspec/04's four engines, reached through appspec/03's [storage] engine
	// key. Only file_system can resolve on a machine with no sync client
	// installed; the other three are observed by WHICH provider their failure
	// names, which is what shows the value selected the resolver it names
	// rather than merely failing.
	for _, c := range []struct{ engine, provider string }{
		{"dropbox", "Dropbox install"},
		{"google_drive", "Google Drive install"},
		{"icloud", "iCloud Drive"},
	} {
		world := NewWorld(t)
		writeConfig(world, "[storage]\nengine = "+c.engine+"\n")
		before := world.Snapshot()

		result := world.Run("list").ExpectExit(1).ExpectSilentStdout()
		text := result.StderrText()
		if want := "Unable to find your " + c.provider + " =("; !strings.Contains(text, want) {
			t.Errorf("engine = %s wrote %q, want it to open with %q", c.engine, result.Stderr, want)
		}
		// appspec/04 requires the message to carry guidance and a
		// documentation URL after the provider line, so it is more than one
		// line -- and appspec/07 gives it the guarded column, so it is a clean
		// exit 1 rather than whatever an unhandled failure would produce.
		if strings.Count(strings.TrimSuffix(text, "\n"), "\n") < 2 {
			t.Errorf("engine = %s wrote %q, want the provider line, guidance, and a documentation pointer", c.engine, result.Stderr)
		}
		world.ExpectUnchanged(before)
	}
}

func TestTheDropboxEngineResolvesFromTheHostDatabase(t *testing.T) {
	// appspec/04 calls the host database's data shape contract: whitespace-
	// separated tokens, at least two, the SECOND strict Base64 decoding to the
	// absolute path of the Dropbox folder.
	//
	// Both halves are observable now that the environment gate reports the
	// root it was handed. The folder the database names is CREATED in the
	// first world, so the run gets all the way through and lists; it is left
	// absent in the second, so the gate names the decoded path back. The
	// second is the half that pins the decoding: a program that resolved some
	// other directory, or the raw Base64, would still list happily in the
	// first world and would name the wrong path here.
	resolved := NewWorld(t)
	writeConfig(resolved, "[storage]\nengine = dropbox\n")
	resolved.WriteFile(".dropbox/host.db", "6103\n"+base64.StdEncoding.EncodeToString([]byte(resolved.Path("Dropbox")))+"\n", 0o600)
	if err := os.MkdirAll(resolved.Path("Dropbox"), 0o700); err != nil {
		t.Fatalf("creating the Dropbox folder: %v", err)
	}
	resolved.Run("list").ExpectExit(0).ExpectStdout(listHeader)

	named := NewWorld(t)
	writeConfig(named, "[storage]\nengine = dropbox\n")
	named.WriteFile(".dropbox/host.db", "6103\n"+base64.StdEncoding.EncodeToString([]byte(named.Path("Elsewhere")))+"\n", 0o600)
	named.Run("list").
		ExpectExit(1).
		ExpectStderrLine(storageFolderRefusal + named.Path("Elsewhere")).
		ExpectSilentStdout()
}

func TestEveryShapeOfBrokenHostDatabaseIsTheSameDropboxFailure(t *testing.T) {
	// appspec/04 lists the causes -- "the file missing or unreadable, fewer
	// than two tokens, or the second token not being valid Base64 / not
	// decodable to text" -- and gives them all one message. A reimplementation
	// that let one of them through would resolve a storage root out of
	// nonsense.
	for _, c := range []struct{ what, content string }{
		{"one token", "6103\n"},
		{"an empty file", ""},
		{"a second token that is not Base64", "6103\nnot base64 at all!\n"},
		{"a second token decoding to nothing", "6103\n\n"},
	} {
		world := NewWorld(t)
		writeConfig(world, "[storage]\nengine = dropbox\n")
		world.WriteFile(".dropbox/host.db", c.content, 0o600)

		world.Run("list").
			ExpectExit(1).
			ExpectStderr("Unable to find your Dropbox install =(").
			ExpectSilentStdout()
	}
}

// driveSupport is the client directory appspec/04 puts both google_drive
// databases in, home-relative.
const driveSupport = "Library/Application Support/Google/Drive"

// installDriveFixture copies one of internal/storage's committed SQLite
// databases into a world.
//
// The fixture is borrowed rather than written here because this program cannot
// produce a SQLite file, and a hand-rolled one would be this suite agreeing
// with the reader it is supposed to be checking from outside.
func installDriveFixture(w *World, fixture, relative string) {
	w.t.Helper()
	root, err := moduleRoot()
	if err != nil {
		w.t.Fatalf("locating the module root: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "internal/storage/testdata", fixture))
	if err != nil {
		w.t.Fatalf("reading the fixture %s: %v", fixture, err)
	}
	w.WriteFile(relative, string(content), 0o600)
}

func TestAGoogleDriveDatabaseThatCannotBeReadIsNotAReasonToTakeTheOtherOne(t *testing.T) {
	// appspec/04 chooses between the two paths by "whichever DB file exists",
	// which is existence and not "whichever query succeeds". The distinction
	// is only visible when the fallback would have worked, so both worlds here
	// carry a VALID database at the fallback path and differ only in what sits
	// at the preferred one.
	t.Run("a directory at the preferred path is not a DB file", func(t *testing.T) {
		// So no database exists there, the fallback is the one that exists,
		// and the run resolves. Which database it resolved FROM is observable
		// now: the fallback fixture names a root of its own, and the gate
		// reports the path it was handed. A program that had somehow resolved
		// the preferred path -- or defaulted -- would name a different one.
		world := NewWorld(t)
		writeConfig(world, "[storage]\nengine = google_drive\n")
		if err := os.MkdirAll(world.Path(driveSupport+"/user_default/sync_config.db"), 0o700); err != nil {
			t.Fatalf("creating the directory fixture: %v", err)
		}
		installDriveFixture(world, "other_root.db", driveSupport+"/sync_config.db")

		world.Run("list").
			ExpectExit(1).
			ExpectStderrLine(storageFolderRefusal + driveFallbackRoot).
			ExpectSilentStdout()
	})
	t.Run("an unreadable file at the preferred path is a DB file", func(t *testing.T) {
		// So it is the one that exists, and failing to read it is the end of
		// the resolution. Falling through would SUCCEED here, which is what
		// makes this observable at all: the two behaviours differ by whether
		// the command runs.
		world := NewWorld(t)
		writeConfig(world, "[storage]\nengine = google_drive\n")
		world.WriteFile(driveSupport+"/user_default/sync_config.db", "this is not a database", 0o600)
		installDriveFixture(world, "other_root.db", driveSupport+"/sync_config.db")
		before := world.Snapshot()

		world.Run("list").
			ExpectExit(1).
			ExpectStderr("Unable to find your Google Drive install =(").
			ExpectSilentStdout()
		world.ExpectUnchanged(before)
	})
}

func TestTheICloudEngineResolvesWhenItsFixedDirectoryExists(t *testing.T) {
	// appspec/04: iCloud Drive is at a fixed location and "the existence of
	// that directory *is* the resolution".
	world := NewWorld(t)
	writeConfig(world, "[storage]\nengine = icloud\n")
	if err := os.MkdirAll(world.Path("Library/Mobile Documents/com~apple~CloudDocs"), 0o700); err != nil {
		t.Fatalf("creating the iCloud directory: %v", err)
	}

	// The directory exists, so it satisfies the environment gate as well as
	// the engine, and the run completes. That is the whole of the engine's
	// contract observed end to end: existence IS the resolution.
	world.Run("list").ExpectExit(0).ExpectStdout(listHeader)
}

func TestTheEnvironmentGateAndNotTheEngineRefusesAPathThatIsNotThere(t *testing.T) {
	// appspec/04 clause 2, the non-uniform postcondition, observed from
	// outside: the user-supplied-path engine returns the path "without any
	// existence check", and a reimplementer is told by name not to add one,
	// because the uniform existence guarantee belongs to the environment gate
	// of appspec/01 section 4.
	//
	// The claim is WHICH STAGE refuses, and there are finally two of them to
	// tell apart. This case was written when there was one -- it asserted that
	// the run got PAST the config gate, which was as far as the deferral could
	// be observed then -- and its own note said to rewrite it here rather than
	// delete it, because the guarantee it defends is not "a nonexistent path
	// is accepted".
	//
	// The two stages are told apart by their diagnostics, which appspec/07
	// puts on different rows: the engine's own failure is the guarded
	// multi-line "Unable to find your <provider> =(", and the gate's is this
	// single line. A program that moved the check into the engine would still
	// exit 1 on this world, which is why the whole line is asserted and not
	// just the failure.
	world := NewWorld(t)
	writeConfig(world, "[storage]\nengine = file_system\npath = nowhere/at/all\n")
	before := world.Snapshot()

	world.Run("list").
		ExpectExit(1).
		ExpectStderrLine(storageFolderRefusal + world.Path("nowhere/at/all")).
		ExpectSilentStdout()
	// appspec/04's relative-path rule read off the same line: a file_system
	// path that is not absolute resolves under HOME, not under the working
	// directory -- and the command runs with its working directory at the
	// world's root, so the two are different directories here.
	world.ExpectUnchanged(before)
}

func TestTheFileSystemEngineWithNoPathIsRefused(t *testing.T) {
	// appspec/04 clause 3's exception, and appspec/07's "file_system engine
	// with no path" row: an unguarded failure. appspec/02 requires an
	// unguarded diagnostic to name the offending value, so the engine whose
	// requirement went unmet is named.
	world := NewWorld(t)
	writeConfig(world, "[storage]\nengine = file_system\n")
	before := world.Snapshot()

	result := world.Run("list").ExpectFailureExit().ExpectSilentStdout()
	if !strings.Contains(result.StderrText(), "file_system") {
		t.Errorf("stderr = %q, want it to name the engine", result.Stderr)
	}
	world.ExpectUnchanged(before)
}

func TestAnUnknownEngineValueIsRefusedAndNamed(t *testing.T) {
	world := NewWorld(t)
	writeConfig(world, unknownEngine("onedrive"))
	before := world.Snapshot()

	result := world.Run("list").ExpectFailureExit().ExpectSilentStdout()
	if !strings.Contains(result.StderrText(), "onedrive") {
		t.Errorf("stderr = %q, want it to name the offending value", result.Stderr)
	}
	world.ExpectUnchanged(before)
}

func TestEngineValuesAreCaseSensitive(t *testing.T) {
	// appspec/03: "Value is matched exactly (case-sensitive)". A program that
	// folded the case would accept "Dropbox" here and resolve an engine the
	// user's file does not name -- silently, and only on the machines where
	// that engine happens to work.
	for _, value := range []string{"Dropbox", "DROPBOX", "File_System", "iCloud"} {
		world := NewWorld(t)
		writeConfig(world, unknownEngine(value)+"path = storage\n")

		result := world.Run("list").ExpectFailureExit().ExpectSilentStdout()
		if !strings.Contains(result.StderrText(), value) {
			t.Errorf("engine = %s wrote %q, want it refused as an unknown engine naming that value", value, result.Stderr)
		}
	}
}

func TestEveryForbiddenStorageDirectoryIsRefusedAndNamed(t *testing.T) {
	// appspec/03: two values are forbidden outright and one of them in three
	// spellings, because "the storage sub-directory may never collide with a
	// custom-apps directory". appspec/07 puts the row in the unguarded column,
	// where the diagnostic names the value.
	for _, directory := range []string{
		".mackup",
		"mackup/applications",
		".config/mackup/applications",
		"somewhere/deeper/.config/mackup/applications",
	} {
		world := NewWorld(t)
		writeConfig(world, "[storage]\nengine = file_system\npath = storage\ndirectory = "+directory+"\n")
		before := world.Snapshot()

		result := world.Run("list").ExpectFailureExit().ExpectSilentStdout()
		if !strings.Contains(result.StderrText(), directory) {
			t.Errorf("directory = %s wrote %q, want it refused and named", directory, result.Stderr)
		}
		world.ExpectUnchanged(before)
	}
}

func TestAnOrdinaryStorageDirectoryIsAccepted(t *testing.T) {
	// The other side of the same rule -- "any other value is accepted
	// verbatim" -- including two values that a substring test for the
	// forbidden ones would reject.
	for _, directory := range []string{"Mackup", "mackup", ".mackup-backup", "config/mackup/applications-old", "my sync folder"} {
		world := NewWorld(t)
		// The storage ROOT, which the environment gate requires to exist. The
		// sub-directory under test is not created: appspec/01 section 4 makes
		// its existence a level-2/3 concern of the sync commands, and `list`
		// has no fifth gate -- so an accepted value is one that lets the run
		// complete without the sub-folder being there at all.
		writeConfig(world, "[storage]\nengine = file_system\npath = storage\ndirectory = "+directory+"\n")
		if err := os.MkdirAll(world.Path("storage"), 0o700); err != nil {
			t.Fatalf("creating the storage root: %v", err)
		}

		world.Run("list").ExpectExit(0).ExpectStdout(listHeader)
	}
}

func TestTheTwoConfigFailureRegimesAreDistinguishable(t *testing.T) {
	// appspec/01 section 6: "which cases fall in which regime is itself
	// contract as observed", and appspec/07's table assigns every config row a
	// column. Both regimes share one post-condition -- no stdout, no
	// filesystem change, non-zero exit -- which is asserted for each row here;
	// what separates them is the diagnostic's shape, which appspec/02 makes
	// machine-readable for the unguarded rows by requiring the offending value
	// in it.
	//
	// Asserted as a pair rather than row by row, because the failure worth
	// catching is the two collapsing into one: a program that gave every
	// startup failure the same shape would satisfy every individual row above
	// and still lose the distinction the specification calls contract.
	for _, c := range []struct {
		what     string
		config   string
		argv     []string
		guarded  bool
		mentions string
	}{
		{"an explicitly named config that is not there", "", []string{"-c", "nope.cfg", "list"}, true, "does not exist"},
		{"a storage engine that cannot find its folder", "[storage]\nengine = icloud\n", []string{"list"}, true, "Unable to find your"},
		{"an unknown engine value", unknownEngine("onedrive"), []string{"list"}, false, "onedrive"},
		{"the file_system engine with no path", "[storage]\nengine = file_system\n", []string{"list"}, false, "file_system"},
		{"a forbidden directory value", "[storage]\nengine = file_system\npath = storage\ndirectory = .mackup\n", []string{"list"}, false, ".mackup"},
	} {
		world := NewWorld(t)
		if c.config != "" {
			writeConfig(world, c.config)
		}
		before := world.Snapshot()

		result := world.Run(c.argv...).ExpectFailureExit().ExpectSilentStdout()
		text := result.StderrText()
		if !strings.Contains(text, c.mentions) {
			t.Errorf("%s: stderr = %q, want %q inside it", c.what, result.Stderr, c.mentions)
		}
		// appspec/07 opens every guarded single-line row with "Error: " and
		// gives the unguarded ones no such shape. This is this program's
		// reading of a distinction the specification permits collapsing --
		// recorded in internal/fault -- and the check is here so the reading
		// cannot be lost silently.
		if guarded := strings.HasPrefix(text, "Error: ") || strings.HasPrefix(text, "Unable to find your "); guarded != c.guarded {
			regime := map[bool]string{true: "guarded", false: "unguarded"}
			t.Errorf("%s: stderr = %q reads as %s, want %s per appspec/07's error table", c.what, result.Stderr, regime[guarded], regime[c.guarded])
		}
		world.ExpectUnchanged(before)
	}
}

func TestEveryConfigFailureIsBrightRedOnEveryLine(t *testing.T) {
	// appspec/07: "Every colored string is terminated with a reset", and
	// fatal errors are bright red. Two of the guarded rows are multi-line
	// blocks, and a block coloured as ONE string leaves its middle lines
	// opening a colour they never close -- which the terminal then carries
	// into whatever prints next. ExpectStderrColor checks line by line, so
	// running the multi-line rows through it is the point of this case.
	for _, c := range []struct {
		what   string
		config string
	}{
		{"the provider block", "[storage]\nengine = icloud\n"},
		{"the legacy-config block", "[storage]\nengine = file_system\npath = storage\n[Allowed Applications]\nvim\n"},
		{"a single-line guarded row", unknownEngine("onedrive")},
	} {
		world := NewWorld(t)
		writeConfig(world, c.config)
		world.Run("list").
			ExpectFailureExit().
			ExpectStderrColor("91").
			ExpectSilentStdout()
	}
}

func TestTheConfigFileFormatIsReadAsAppspec03Describes(t *testing.T) {
	// The parsing rules of appspec/03 "File format", observed through the one
	// value that reaches the boundary. Each fixture writes a comment, an odd
	// whitespace arrangement, or an unknown section around the engine key; a
	// parser that mishandled any of them reports a DIFFERENT engine value --
	// or none, which shows up as the Dropbox default.
	//
	// The whole diagnostic is asserted, not "from-config" inside it, and the
	// difference is the one the mutation battery found: a parser that stopped
	// stripping comments reports the engine as "from-config ; a comment", which
	// CONTAINS the wanted substring, so every comment fixture here passed over a
	// dialect that had lost the rule they exist to check. Observed, by removing
	// stripComment and watching the conformance suite stay green. The same
	// vacuity ExpectStdoutLine's comment records for the version value, in the
	// other stream.
	for _, c := range []struct{ what, config string }{
		{"an inline semicolon comment", "[storage]\nengine = from-config ; a comment\n"},
		{"an inline hash comment", "[storage]\nengine = from-config # a comment\n"},
		{"whole-line comments", "; a comment\n# another\n[storage]\nengine = from-config\n"},
		{"whitespace around the key and value", "  [storage]  \n\tengine   =   from-config  \n"},
		{"an unknown section before it", "[whatever]\nkey = value\n[storage]\nengine = from-config\n"},
		{"an unknown section after it", "[storage]\nengine = from-config\n[whatever]\nkey = value\n"},
		{"a differently-cased key", "[storage]\nENGINE = from-config\n"},
		{"blank lines throughout", "\n\n[storage]\n\nengine = from-config\n\n"},
		{"a comment on a bare key", "[applications_to_ignore]\nvim ; a comment\n[storage]\nengine = from-config\n"},
	} {
		world := NewWorld(t)
		writeConfig(world, c.config)

		result := world.Run("list").ExpectFailureExit().ExpectSilentStdout()
		if got, want := result.StderrText(), "mackup: Unknown storage engine: from-config\n"; got != want {
			t.Errorf("%s: stderr = %q, want exactly %q: the engine value read verbatim and nothing else", c.what, result.Stderr, want)
		}
	}
}

func TestADifferentlyCasedSectionNameIsNotTheStorageSection(t *testing.T) {
	// appspec/03: "Section presence is by exact name." A [Storage] header
	// names some other, unknown section, which the same paragraph says is
	// ignored -- so the engine falls back to the Dropbox default rather than
	// being read from it.
	world := NewWorld(t)
	writeConfig(world, "[Storage]\nengine = from-config\n")

	world.Run("list").
		ExpectExit(1).
		ExpectStderr("Unable to find your Dropbox install =(").
		ExpectSilentStdout()
}
