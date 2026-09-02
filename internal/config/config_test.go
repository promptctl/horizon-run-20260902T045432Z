package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/promptctl/macklebox/internal/fault"
)

// A world is one throwaway home directory and the environment a case loads a
// config against.
//
// Environment is a value rather than the process's own, so a case states the
// variables it is about instead of mutating os.Environ -- which would make
// these cases order-dependent and unable to run in parallel with each other.
type world struct {
	t   *testing.T
	env Environment
}

// newWorld returns a world whose home directory is empty and whose MACKUP_CONFIG
// and XDG_CONFIG_HOME are unset.
func newWorld(t *testing.T) *world {
	t.Helper()
	return &world{t: t, env: Environment{Home: t.TempDir()}}
}

// write creates a home-relative file with the given content, making its parent
// directories, and returns its absolute path.
func (w *world) write(relative, content string) string {
	w.t.Helper()
	path := filepath.Join(w.env.Home, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		w.t.Fatalf("creating the parent of %s: %v", relative, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		w.t.Fatalf("writing %s: %v", relative, err)
	}
	return path
}

// usableStorage is a [storage] section that resolves without any sync client
// installed, so a case about something else does not also have to be about the
// Dropbox engine. It names the file_system engine, whose path appspec/04
// deliberately does not check the existence of.
const usableStorage = "[storage]\nengine = file_system\npath = storage\n"

// load resolves a config for this world, with no -c option.
func (w *world) load() (*Config, error) {
	w.t.Helper()
	return Load(Override{}, w.env)
}

// mustLoad resolves a config and fails the case if it does not.
func (w *world) mustLoad() *Config {
	w.t.Helper()
	cfg, err := w.load()
	if err != nil {
		w.t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestAnAbsentConfigFileAppliesEveryDefault(t *testing.T) {
	// appspec/03 "Absent / empty": a nonexistent file "parses to 'no sections
	// set'", and then "all defaults apply: engine = dropbox, directory =
	// Mackup, no apps ignored, no apps allow-listed". The engine default is
	// observable here as the failure it causes -- the same paragraph says an
	// empty config "then fails at storage-location resolution ... and exits 1"
	// on a machine with no Dropbox folder.
	world := newWorld(t)
	_, err := world.load()
	if err == nil {
		t.Fatal("Load with no config file succeeded; with no Dropbox install the default engine cannot resolve")
	}
	if !strings.Contains(err.Error(), "Dropbox") {
		t.Errorf("Load = %q, want the Dropbox engine's failure: appspec/03 makes dropbox the default", err)
	}
}

func TestAnEmptyConfigFileAppliesEveryDefault(t *testing.T) {
	// The other half of appspec/03's "nonexistent OR empty". An empty file
	// must not be a parse error, and must reach the same place an absent one
	// does.
	world := newWorld(t)
	world.write(defaultConfigName, "")
	if _, err := world.load(); err == nil || !strings.Contains(err.Error(), "Dropbox") {
		t.Errorf("Load on an empty config = %v, want the default engine's failure", err)
	}
}

func TestTheDefaultsAreVisibleOnceStorageResolves(t *testing.T) {
	// The defaults appspec/03 lists that are NOT the engine: directory
	// "Mackup", no apps ignored, no apps allow-listed. Asserted against a
	// config that sets only the engine, so each one is a default rather than
	// an echo of the fixture.
	world := newWorld(t)
	world.write(defaultConfigName, usableStorage)
	cfg := world.mustLoad()

	if want := filepath.Join(world.env.Home, "storage"); cfg.Root() != want {
		t.Errorf("Root = %q, want %q", cfg.Root(), want)
	}
	if cfg.Directory() != defaultDirectory {
		t.Errorf("Directory = %q, want %q", cfg.Directory(), defaultDirectory)
	}
	if want := filepath.Join(world.env.Home, "storage", "Mackup"); cfg.MackupFolder() != want {
		t.Errorf("MackupFolder = %q, want %q -- appspec/03's derived property is the root joined to the sub-directory", cfg.MackupFolder(), want)
	}
	if len(cfg.Ignored()) != 0 || len(cfg.Allowed()) != 0 {
		t.Errorf("Ignored = %v and Allowed = %v, want both empty", cfg.Ignored(), cfg.Allowed())
	}
}

func TestTheHomeConfigWinsOverBothEnvironmentCandidates(t *testing.T) {
	// appspec/03's first observed precedence fact, stated as an experiment
	// rather than as an ordering: "with a ~/.mackup.cfg present, it is used
	// even when MACKUP_CONFIG and XDG_CONFIG_HOME both point at other EXISTING
	// config files."
	//
	// Which file was read is observed through the sub-directory each one sets,
	// so a case fails with the name of the file that was wrongly chosen rather
	// than with a bare mismatch.
	world := newWorld(t)
	world.write(defaultConfigName, usableStorage+"directory = from-home\n")
	world.env.MackupConfig = world.write("named.cfg", usableStorage+"directory = from-mackup-config\n")
	world.env.XDGConfigHome = filepath.Join(world.env.Home, "xdg")
	world.write(filepath.Join("xdg", xdgConfigName), usableStorage+"directory = from-xdg\n")

	if got := world.mustLoad().Directory(); got != "from-home" {
		t.Errorf("the config read was %q, want the home directory's ~/.mackup.cfg", got)
	}
}

func TestMackupConfigWinsOverTheXDGCandidate(t *testing.T) {
	// appspec/03's second observed fact: "with no ~/.mackup.cfg,
	// XDG_CONFIG_HOME's file is used if present; if MACKUP_CONFIG also names
	// an existing file, MACKUP_CONFIG wins over the XDG candidate (it is
	// checked earlier in the list)."
	world := newWorld(t)
	world.env.MackupConfig = world.write("named.cfg", usableStorage+"directory = from-mackup-config\n")
	world.env.XDGConfigHome = filepath.Join(world.env.Home, "xdg")
	world.write(filepath.Join("xdg", xdgConfigName), usableStorage+"directory = from-xdg\n")

	if got := world.mustLoad().Directory(); got != "from-mackup-config" {
		t.Errorf("the config read was %q, want MACKUP_CONFIG's file", got)
	}
}

func TestTheXDGCandidateIsUsedWhenItIsTheOnlyOne(t *testing.T) {
	world := newWorld(t)
	world.env.XDGConfigHome = filepath.Join(world.env.Home, "xdg")
	world.write(filepath.Join("xdg", xdgConfigName), usableStorage+"directory = from-xdg\n")

	if got := world.mustLoad().Directory(); got != "from-xdg" {
		t.Errorf("the config read was %q, want the XDG candidate", got)
	}
}

func TestTheXDGBaseDefaultsToDotConfigInHome(t *testing.T) {
	// appspec/03: "If XDG_CONFIG_HOME is unset, the base defaults to ~/.config"
	// -- and the filename there drops the leading dot, which the spec calls
	// out because it is the detail a reimplementation gets wrong.
	world := newWorld(t)
	world.write(filepath.Join(".config", xdgConfigName), usableStorage+"directory = from-default-xdg\n")

	if got := world.mustLoad().Directory(); got != "from-default-xdg" {
		t.Errorf("the config read was %q, want ~/.config/mackup/mackup.cfg", got)
	}
	// The other half: the dotted spelling of that filename is NOT a candidate,
	// so a reader that reused ".mackup.cfg" under the XDG base finds nothing.
	other := newWorld(t)
	other.write(filepath.Join(".config", "mackup", ".mackup.cfg"), usableStorage+"directory = wrong-name\n")
	if _, err := other.load(); err == nil || !strings.Contains(err.Error(), "Dropbox") {
		t.Errorf("Load = %v, want the default engine's failure: ~/.config/mackup/.mackup.cfg is not a candidate", err)
	}
}

func TestAnEmptyMackupConfigVariableIsSkipped(t *testing.T) {
	// appspec/03: "If MACKUP_CONFIG is unset or empty, this candidate is
	// skipped." An empty value expanded as a path is the working directory,
	// which would make the candidate depend on where the program was started.
	world := newWorld(t)
	world.env.MackupConfig = ""
	world.env.XDGConfigHome = filepath.Join(world.env.Home, "xdg")
	world.write(filepath.Join("xdg", xdgConfigName), usableStorage+"directory = from-xdg\n")

	if got := world.mustLoad().Directory(); got != "from-xdg" {
		t.Errorf("the config read was %q, want the XDG candidate", got)
	}
}

func TestATildeInMackupConfigIsExpanded(t *testing.T) {
	world := newWorld(t)
	world.write("named.cfg", usableStorage+"directory = from-mackup-config\n")
	world.env.MackupConfig = "~/named.cfg"

	if got := world.mustLoad().Directory(); got != "from-mackup-config" {
		t.Errorf("the config read was %q, want the tilde-expanded MACKUP_CONFIG file", got)
	}
}

func TestACandidateThatIsNotARegularFileIsSkipped(t *testing.T) {
	// appspec/03 says discovery takes "the first that EXISTS AS A REGULAR
	// FILE", so a directory sitting at a candidate path is not a config file
	// and must not stop the search. A reader that only tested for existence
	// would stop here and then fail trying to read a directory.
	world := newWorld(t)
	if err := os.MkdirAll(filepath.Join(world.env.Home, defaultConfigName), 0o700); err != nil {
		t.Fatalf("creating the fixture: %v", err)
	}
	world.env.XDGConfigHome = filepath.Join(world.env.Home, "xdg")
	world.write(filepath.Join("xdg", xdgConfigName), usableStorage+"directory = from-xdg\n")

	if got := world.mustLoad().Directory(); got != "from-xdg" {
		t.Errorf("the config read was %q, want the XDG candidate", got)
	}
}

func TestAnExplicitPathSkipsDiscoveryEntirely(t *testing.T) {
	// appspec/03 "Explicit override": "discovery is skipped and <path> is used
	// directly". So a ~/.mackup.cfg that would otherwise win is not consulted.
	world := newWorld(t)
	world.write(defaultConfigName, usableStorage+"directory = from-home\n")
	chosen := world.write("elsewhere.cfg", usableStorage+"directory = from-c\n")

	cfg, err := Load(Override{Path: chosen, Set: true}, world.env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Directory() != "from-c" {
		t.Errorf("the config read was %q, want the explicitly named file", cfg.Directory())
	}
}

func TestAnExplicitRelativePathResolvesAgainstHomeNotTheWorkingDirectory(t *testing.T) {
	// appspec/03's surprising rule, and the reason an explicitly named config
	// is subject to the containment check at all: "a RELATIVE path is resolved
	// relative to the home directory (not the current working directory). So
	// -c foo.cfg means ~/foo.cfg."
	//
	// A file of the same name is planted in the working directory, so a
	// resolver that used the cwd finds one and reads the wrong config rather
	// than failing.
	world := newWorld(t)
	world.write("foo.cfg", usableStorage+"directory = from-home\n")

	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "foo.cfg"), []byte(usableStorage+"directory = from-cwd\n"), 0o600); err != nil {
		t.Fatalf("writing the decoy: %v", err)
	}
	t.Chdir(elsewhere)

	cfg, err := Load(Override{Path: "foo.cfg", Set: true}, world.env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Directory() != "from-home" {
		t.Errorf("the config read was %q, want ~/foo.cfg", cfg.Directory())
	}
}

func TestAnExplicitTildePathIsExpanded(t *testing.T) {
	world := newWorld(t)
	world.write("under-home.cfg", usableStorage+"directory = expanded\n")

	cfg, err := Load(Override{Path: "~/under-home.cfg", Set: true}, world.env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Directory() != "expanded" {
		t.Errorf("the config read was %q, want the tilde-expanded file", cfg.Directory())
	}
}

func TestAnExplicitFileThatDoesNotExistIsTheGuardedRefusal(t *testing.T) {
	// appspec/07's table gives this row a literal shape: "Error: The config
	// file '<p>' does not exist. Aborting." -- and the path in it is the
	// ABSOLUTE one, which for `-c nope.cfg` is the home-relative resolution,
	// not the two words the user typed.
	world := newWorld(t)
	_, err := Load(Override{Path: "nope.cfg", Set: true}, world.env)
	want := "Error: The config file '" + filepath.Join(world.env.Home, "nope.cfg") + "' does not exist. Aborting."
	expectGuarded(t, err, want)
}

func TestAnExplicitPathOutsideHomeIsTheGuardedRefusal(t *testing.T) {
	// appspec/03 "Home-directory containment", with the specification's own
	// example. The file exists, so this cannot be the missing-file row: the
	// two are distinct rows of appspec/07's table and a reader that checked
	// containment first would report the wrong one.
	world := newWorld(t)
	outside := filepath.Join(t.TempDir(), "outside.cfg")
	if err := os.WriteFile(outside, []byte(usableStorage), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	_, err := Load(Override{Path: outside, Set: true}, world.env)
	expectGuarded(t, err, "Error: The config file '"+outside+"' is not in your home directory. Aborting.")
}

func TestADiscoveredPathOutsideHomeIsRefusedToo(t *testing.T) {
	// appspec/03: the containment check is made "at construction
	// independently of whether the file was discovered or explicitly named".
	// MACKUP_CONFIG is the only candidate that can point outside home, so it
	// is the only way to observe that half -- and a check written inside the
	// -c branch passes every other case in this file.
	world := newWorld(t)
	outside := filepath.Join(t.TempDir(), "outside.cfg")
	if err := os.WriteFile(outside, []byte(usableStorage), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	world.env.MackupConfig = outside

	_, err := world.load()
	expectGuarded(t, err, "Error: The config file '"+outside+"' is not in your home directory. Aborting.")
}

func TestAPathThatMerelyBeginsWithTwoDotsIsInsideHome(t *testing.T) {
	// The containment check compares path ELEMENTS, not string prefixes: a
	// config at ~/..config is plainly inside the home directory, and this
	// program manages dotted names for a living.
	world := newWorld(t)
	world.write("..config", usableStorage+"directory = dotted\n")

	cfg, err := Load(Override{Path: "..config", Set: true}, world.env)
	if err != nil {
		t.Fatalf("Load: %v, want ~/..config accepted as inside the home directory", err)
	}
	if cfg.Directory() != "dotted" {
		t.Errorf("the config read was %q, want ~/..config", cfg.Directory())
	}
}

func TestAnEmptyExplicitPathIsAPathTheUserNamed(t *testing.T) {
	// The distinction cli.Options preserves and this package needs: `-c ""` is
	// not "no option given". It resolves to the home directory itself, which
	// is not a regular file, so it takes the missing-file row rather than
	// silently falling back to discovery -- which would read a ~/.mackup.cfg
	// the user did not ask for.
	world := newWorld(t)
	world.write(defaultConfigName, usableStorage+"directory = from-home\n")

	_, err := Load(Override{Path: "", Set: true}, world.env)
	expectGuarded(t, err, "Error: The config file '"+world.env.Home+"' does not exist. Aborting.")
}

// expectGuarded asserts that an error is a guarded failure carrying exactly
// the diagnostic appspec/07's table gives its row.
func expectGuarded(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Load succeeded, want %q", want)
	}
	regime, declared := fault.RegimeOf(err)
	if !declared {
		t.Fatalf("Load = %v, an unclassified error; appspec/07 puts this row in the guarded column", err)
	}
	if regime != fault.Guarded {
		t.Errorf("Load failed %v, want guarded", regime)
	}
	if got := err.Error(); got != want {
		t.Errorf("Load = %q, want %q", got, want)
	}
}

// expectUnguarded asserts that an error is an unguarded failure whose
// diagnostic names the offending value, as appspec/02 requires of that regime.
func expectUnguarded(t *testing.T, err error, offending string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Load succeeded, want an unguarded failure naming %q", offending)
	}
	regime, declared := fault.RegimeOf(err)
	if !declared {
		t.Fatalf("Load = %v, an unclassified error; appspec/07 puts this row in the unguarded column", err)
	}
	if regime != fault.Unguarded {
		t.Errorf("Load failed %v, want unguarded: appspec/01 section 6 makes which regime a case falls in contract", regime)
	}
	if !strings.Contains(err.Error(), offending) {
		t.Errorf("Load = %q, want it to name the offending value %q", err, offending)
	}
}

func TestAnUnknownEngineIsTheUnguardedRefusal(t *testing.T) {
	// appspec/03 gives the diagnostic literally ("Unknown storage engine:
	// <value>") and appspec/07 puts the row in the unguarded column. Both are
	// checked, because getting the text right and the regime wrong is exactly
	// what the done-claim of this ticket rules out.
	world := newWorld(t)
	world.write(defaultConfigName, "[storage]\nengine = onedrive\n")

	_, err := world.load()
	expectUnguarded(t, err, "onedrive")
	if !strings.Contains(err.Error(), "Unknown storage engine: onedrive") {
		t.Errorf("Load = %q, want appspec/03's literal diagnostic", err)
	}
}

func TestEngineValuesAreCaseSensitive(t *testing.T) {
	// appspec/03: "Values in [storage] are case-sensitive". A reader that
	// folded the case would accept "Dropbox" and then resolve an engine the
	// user's file does not name -- silently, on the machine where Dropbox
	// happens to be installed.
	world := newWorld(t)
	world.write(defaultConfigName, "[storage]\nengine = File_System\npath = storage\n")

	_, err := world.load()
	expectUnguarded(t, err, "File_System")
}

func TestFileSystemWithNoPathIsTheUnguardedRefusal(t *testing.T) {
	world := newWorld(t)
	world.write(defaultConfigName, "[storage]\nengine = file_system\n")

	expectUnguarded(t, mustFail(t, world), "file_system")
}

// mustFail loads the world's config and returns the error, failing the case if
// the load succeeded.
func mustFail(t *testing.T, w *world) error {
	t.Helper()
	cfg, err := w.load()
	if err == nil {
		t.Fatalf("Load succeeded, resolving %q", cfg.MackupFolder())
	}
	return err
}

func TestEveryForbiddenDirectoryValueIsRefused(t *testing.T) {
	// appspec/03 forbids exactly these, and gives the rule behind them: "the
	// storage sub-directory may never collide with a custom-apps directory."
	// The suffix form is the specification's own generalization.
	for _, directory := range []string{
		".mackup",
		"mackup/applications",
		".config/mackup/applications",
		"somewhere/deeper/.config/mackup/applications",
	} {
		world := newWorld(t)
		world.write(defaultConfigName, usableStorage+"directory = "+directory+"\n")
		expectUnguarded(t, mustFail(t, world), directory)
	}
}

func TestAnOrdinaryDirectoryValueIsAcceptedVerbatim(t *testing.T) {
	// The other side of the rule: "any other value is accepted verbatim", so
	// a name that merely resembles a forbidden one is not refused. A
	// substring test in place of the exact-and-suffix rule would reject the
	// first two here.
	for _, directory := range []string{"Mackup", "dotfiles", "mackup", ".mackup-backup", "config/mackup/applications-old", "sync files"} {
		world := newWorld(t)
		world.write(defaultConfigName, usableStorage+"directory = "+directory+"\n")
		cfg, err := world.load()
		if err != nil {
			t.Errorf("Load with directory = %q: %v", directory, err)
			continue
		}
		if cfg.Directory() != directory {
			t.Errorf("Directory = %q, want the configured %q verbatim", cfg.Directory(), directory)
		}
	}
}

func TestALegacySectionAbortsTheLoad(t *testing.T) {
	// appspec/03 "Legacy config rejection", appspec/07's "legacy config
	// sections present" row: a GUARDED, multi-line refusal. It happens during
	// config load, so it blocks every command.
	for _, section := range []string{legacyAllowedSection, legacyIgnoredSection} {
		world := newWorld(t)
		world.write(defaultConfigName, usableStorage+"\n["+section+"]\nvim\n")

		err := mustFail(t, world)
		regime, declared := fault.RegimeOf(err)
		if !declared || regime != fault.Guarded {
			t.Errorf("[%s]: Load failed %v (declared %v), want guarded", section, regime, declared)
		}
		if !strings.Contains(err.Error(), "["+section+"]") {
			t.Errorf("[%s]: Load = %q, want it to name the section it found", section, err)
		}
		if lines := strings.Count(err.Error(), "\n") + 1; lines < 2 {
			t.Errorf("[%s]: Load = %q, which is one line; appspec/03 gives this a multi-line message", section, err)
		}
	}
}

func TestALegacySectionIsRefusedBeforeTheStorageSectionIsRead(t *testing.T) {
	// The order matters and is not incidental: appspec/03's whole reason for
	// this refusal is that the program "will do nothing rather than act
	// incorrectly" on a file it may read wrongly. A load that validated
	// storage first would report an unknown engine -- or worse, resolve one --
	// for a file it is about to refuse to read at all.
	world := newWorld(t)
	world.write(defaultConfigName, "[storage]\nengine = onedrive\n[Ignored Applications]\nvim\n")

	err := mustFail(t, world)
	if !strings.Contains(err.Error(), "Ignored Applications") {
		t.Errorf("Load = %q, want the legacy refusal rather than the engine's", err)
	}
}

func TestTheApplicationListsAreReadAsSets(t *testing.T) {
	world := newWorld(t)
	world.write(defaultConfigName, usableStorage+`
[applications_to_sync]
vim
git

[applications_to_ignore]
ssh
`)
	cfg := world.mustLoad()

	if want := []string{"git", "vim"}; !reflect.DeepEqual(cfg.Allowed(), want) {
		t.Errorf("Allowed = %v, want %v", cfg.Allowed(), want)
	}
	if want := []string{"ssh"}; !reflect.DeepEqual(cfg.Ignored(), want) {
		t.Errorf("Ignored = %v, want %v", cfg.Ignored(), want)
	}
}

func TestApplicationListKeysAreLowercased(t *testing.T) {
	// One half of appspec/03's cross-component case-policy pair: "config
	// application-list keys are case-normalized; definition file paths are
	// case-exact". The spec warns that "a reimplementation that lowercases
	// file paths, or preserves case in config keys, breaks matching in a way
	// neither section alone makes obvious", so the half that lives here is
	// pinned here.
	world := newWorld(t)
	world.write(defaultConfigName, usableStorage+"\n[applications_to_sync]\nVim\nSublime-Text-3\n[applications_to_ignore]\nSSH\n")
	cfg := world.mustLoad()

	if want := []string{"sublime-text-3", "vim"}; !reflect.DeepEqual(cfg.Allowed(), want) {
		t.Errorf("Allowed = %v, want %v", cfg.Allowed(), want)
	}
	if want := []string{"ssh"}; !reflect.DeepEqual(cfg.Ignored(), want) {
		t.Errorf("Ignored = %v, want %v", cfg.Ignored(), want)
	}
}

func TestTheDenylistWinsOverTheAllowlist(t *testing.T) {
	// appspec/03 "Combined precedence": start from the allowlist if it is
	// present and non-empty, then remove everything in the denylist -- "so an
	// app appearing in BOTH lists is ignored". That is the one a
	// reimplementation is likely to get backwards, and it is invisible unless
	// a case puts the same key in both.
	world := newWorld(t)
	world.write(defaultConfigName, usableStorage+"\n[applications_to_sync]\nvim\ngit\nssh\n[applications_to_ignore]\nssh\n")

	all := []string{"bash", "git", "ssh", "vim"}
	if want := []string{"git", "vim"}; !reflect.DeepEqual(world.mustLoad().Scope(all), want) {
		t.Errorf("Scope = %v, want %v", world.mustLoad().Scope(all), want)
	}
}

func TestScopeStartsFromEveryKeyWhenNoAllowlistIsSet(t *testing.T) {
	world := newWorld(t)
	world.write(defaultConfigName, usableStorage+"\n[applications_to_ignore]\nssh\n")

	all := []string{"bash", "git", "ssh", "vim"}
	if want := []string{"bash", "git", "vim"}; !reflect.DeepEqual(world.mustLoad().Scope(all), want) {
		t.Errorf("Scope = %v, want %v", world.mustLoad().Scope(all), want)
	}
}

func TestAnEmptyAllowlistSectionMeansAllApplications(t *testing.T) {
	// appspec/03 narrows the scope only when the section is "present and
	// NON-EMPTY". A reader that treated a present-but-empty section as an
	// allowlist of nothing would make an empty header silently disable every
	// sync command.
	world := newWorld(t)
	world.write(defaultConfigName, usableStorage+"\n[applications_to_sync]\n")

	all := []string{"git", "vim"}
	if got := world.mustLoad().Scope(all); !reflect.DeepEqual(got, all) {
		t.Errorf("Scope = %v, want every key %v", got, all)
	}
}

func TestScopeNeverInventsAKeyTheDatabaseDoesNotHave(t *testing.T) {
	world := newWorld(t)
	world.write(defaultConfigName, usableStorage+"\n[applications_to_sync]\nvim\nnot-an-application\n")

	if want := []string{"vim"}; !reflect.DeepEqual(world.mustLoad().Scope([]string{"git", "vim"}), want) {
		t.Errorf("Scope = %v, want %v: an allowlisted key with no definition is not an application", world.mustLoad().Scope([]string{"git", "vim"}), want)
	}
}

func TestAnUnsetHomeIsTheUnguardedFailure(t *testing.T) {
	// appspec/03's environment table: HOME "must be set for the program to
	// function; if unset, home-relative operations fail with an uncaught error
	// (nonzero exit)". A relative HOME is refused in the same regime, because
	// every path the program later writes to would otherwise depend on the
	// working directory it was started in.
	for _, home := range []string{"", "relative/home"} {
		_, err := Load(Override{}, Environment{Home: home})
		if err == nil {
			t.Errorf("Load with HOME=%q succeeded", home)
			continue
		}
		if regime, declared := fault.RegimeOf(err); !declared || regime != fault.Unguarded {
			t.Errorf("Load with HOME=%q failed %v (declared %v), want unguarded", home, regime, declared)
		}
	}
}

func TestAConfigFileThatCannotBeReadIsNotTreatedAsEmpty(t *testing.T) {
	// An unreadable file is not an absent one. Treating it as empty would
	// apply the Dropbox default to a machine whose config selects some other
	// engine -- acting on a configuration the user did not write, which is the
	// same hazard the legacy-section refusal exists for.
	world := newWorld(t)
	path := world.write(defaultConfigName, usableStorage)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatalf("making the config unreadable: %v", err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })
	if os.Geteuid() == 0 {
		t.Skip("running as root, which can read a mode-0 file, so this case cannot make one unreadable")
	}

	err := mustFail(t, world)
	if strings.Contains(err.Error(), "Dropbox") {
		t.Errorf("Load = %q, want a diagnostic about the unreadable file rather than the default engine's failure", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("Load = %q, want it to name the file it could not read", err)
	}
}

func TestEachEngineIsSelectedByItsConfigValue(t *testing.T) {
	// The join between appspec/03's [storage] engine key and appspec/04's four
	// resolvers. Only file_system can succeed with no sync client installed;
	// the other three are observed by which provider their failure names,
	// which is what proves the value selected the resolver it names.
	for _, c := range []struct{ engine, provider string }{
		{"dropbox", "Dropbox install"},
		{"google_drive", "Google Drive install"},
		{"icloud", "iCloud Drive"},
	} {
		world := newWorld(t)
		world.write(defaultConfigName, "[storage]\nengine = "+c.engine+"\n")
		err := mustFail(t, world)
		if !strings.Contains(err.Error(), "Unable to find your "+c.provider) {
			t.Errorf("engine = %s failed with %q, want the %s resolver's message", c.engine, err, c.provider)
		}
	}
}

func TestThePathKeyIsIgnoredByTheAutoDetectingEngines(t *testing.T) {
	// appspec/03: path is "ignored (not required) for the three auto-detected
	// engines". So a config that sets both is not an error and the path is
	// simply unused -- a reader that took the path whenever it was present
	// would resolve a storage root the engine never chose.
	world := newWorld(t)
	world.write(defaultConfigName, "[storage]\nengine = icloud\npath = storage\n")

	err := mustFail(t, world)
	if !strings.Contains(err.Error(), "iCloud Drive") {
		t.Errorf("Load = %q, want the iCloud resolver's failure rather than the configured path", err)
	}
}
