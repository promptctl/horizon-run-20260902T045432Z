package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/promptctl/macklebox/internal/ui"
	"github.com/promptctl/macklebox/internal/version"
)

// absentStorageConfig is a config file in the package's home directory whose
// storage engine resolves to absentStorageDirectory, which is never created.
// Named with -c, it is how a case reaches the environment gate's storage-root
// arm without disturbing the config every other case relies on.
const (
	absentStorageConfig    = "absent-storage.cfg"
	absentStorageDirectory = "nowhere-at-all"
)

// TestMain gives the whole package one throwaway home directory to run in.
//
// Main resolves the config and the storage root from the REAL environment --
// that is what EnvironmentFromOS is -- so before this existed, every case that
// got past appspec/02's config gate was reading the developer's machine. A
// developer with a ~/.mackup.cfg naming a folder that exists saw dispatch; CI,
// which has neither that file nor a Dropbox install, saw the provider failure
// and reported a defect that was only ever in the test. The environment a case
// is about is not the one it inherits.
//
// The config written here is the least a case needs to get PAST the gate: an
// engine whose resolution cannot fail for an absent provider. Cases that are
// about config failures belong in internal/config, which states its
// environment per case, and at the boundary in test/conformance.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "macklebox-app-home-")
	if err != nil {
		panic(err)
	}
	config := "[storage]\nengine = file_system\npath = storage\n"
	if err := os.WriteFile(filepath.Join(home, ".mackup.cfg"), []byte(config), 0o600); err != nil {
		panic(err)
	}
	// And the storage root itself, which the environment gate of appspec/01
	// section 4 requires to exist before any command runs. The config alone
	// gets a run past the config gate and no further: appspec/04's
	// file_system engine deliberately does not check that its path is there,
	// so the directory is what separates "resolvable" from "usable".
	if err := os.MkdirAll(filepath.Join(home, "storage"), 0o700); err != nil {
		panic(err)
	}
	// A second config, named only through -c, whose storage root is not there.
	// The environment gate is the one stage that can refuse a resolvable
	// config (appspec/04 clause 2 leaves the existence check to it), so a case
	// about the gate needs a config that loads and a root that does not exist
	// -- and writing it here rather than per case keeps the package's one
	// throwaway home the only directory these cases touch.
	absent := "[storage]\nengine = file_system\npath = " + absentStorageDirectory + "\n"
	if err := os.WriteFile(filepath.Join(home, absentStorageConfig), []byte(absent), 0o600); err != nil {
		panic(err)
	}
	// Both of the other two discovery candidates are cleared, not just HOME:
	// a developer with MACKUP_CONFIG exported would otherwise reach a config
	// outside this directory and the isolation would be partial, which is the
	// same defect in a rarer environment.
	os.Setenv("HOME", home)
	os.Unsetenv("MACKUP_CONFIG")
	os.Unsetenv("XDG_CONFIG_HOME")

	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

type result struct {
	code int
	// stdout and stderr are the RAW bytes, colour included. Raw rather than
	// stripped, so an emptiness assertion still means what it says: a stream
	// carrying nothing but escape sequences is not empty, and a result type
	// that stripped on the way in would report it as such.
	stdout string
	stderr string
}

// outText and errText are the messages without their colour. appspec/07 makes
// the level a property of the message and Colorize wraps a whole message
// without splitting it, so stripping gives the exact text back -- which is
// what lets a case assert a literal contract token against output that
// appspec/02 requires to be a coloured diagnostic line.
//
// Read through ui.StripColor rather than a pattern of this package's own: the
// program's definition of what colour is, used from the other end, so the two
// cannot drift.
func (r result) outText() string { return ui.StripColor(r.stdout) }
func (r result) errText() string { return ui.StripColor(r.stderr) }

func run(argv ...string) result {
	var out, errb bytes.Buffer
	code := Main(argv, &ui.IO{In: strings.NewReader(""), Out: &out, Err: &errb})
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

func TestTheseCasesRunAgainstAThrowawayHomeNotTheDevelopers(t *testing.T) {
	// The assertion that keeps TestMain above from being deleted as ceremony.
	// Without it, removing the isolation passes on any machine that happens to
	// have a working ~/.mackup.cfg -- which is exactly how the dependence
	// reached CI in the first place, green on the author's machine and red on
	// a runner with no config and no Dropbox.
	home := os.Getenv("HOME")
	if _, err := os.Stat(filepath.Join(home, ".mackup.cfg")); err != nil {
		t.Fatalf("HOME is %q, which holds no .mackup.cfg: these cases are reading a real machine", home)
	}
	if info, err := os.Stat(filepath.Join(home, "storage")); err != nil || !info.IsDir() {
		t.Fatalf("HOME is %q, whose storage root is not a directory: every case below would stop at the environment gate", home)
	}
	if os.Getenv("MACKUP_CONFIG") != "" || os.Getenv("XDG_CONFIG_HOME") != "" {
		t.Error("a discovery variable is set, so the config read is not this package's")
	}
}

func TestHelpGoesToStdoutAndExitsZero(t *testing.T) {
	for _, argv := range [][]string{{"-h"}, {"--help"}} {
		got := run(argv...)
		if got.code != ExitOK {
			t.Errorf("mackup %s exit = %d, want %d", argv, got.code, ExitOK)
		}
		if !strings.Contains(got.stdout, "Usage:") {
			t.Errorf("mackup %s stdout = %q, want the usage block", argv, got.stdout)
		}
		if got.stderr != "" {
			t.Errorf("mackup %s stderr = %q, want empty", argv, got.stderr)
		}
	}
}

func TestVersionPrintsTheMackupLineToStdout(t *testing.T) {
	got := run("--version")
	if got.code != ExitOK {
		t.Errorf("exit = %d, want %d", got.code, ExitOK)
	}
	if !regexp.MustCompile(`^Mackup \S+\n$`).MatchString(got.outText()) {
		t.Errorf("stdout = %q, want a single \"Mackup <version>\" line", got.stdout)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty", got.stderr)
	}
}

func TestHelpAndVersionShortCircuitBeforeAnythingElse(t *testing.T) {
	// appspec/02: they take no other action -- not even the force conflict
	// check reached below them.
	if got := run("--help", "--force", "--force-no", "backup"); got.code != ExitOK || !strings.Contains(got.stdout, "Usage:") {
		t.Errorf("help with conflicting force flags = %+v, want usage on stdout exit 0", got)
	}
	if got := run("--version", "list"); got.code != ExitOK || !strings.HasPrefix(got.outText(), "Mackup ") {
		t.Errorf("version with a subcommand = %+v, want the version line exit 0", got)
	}
}

func TestConflictingForceFlagsEmitTheLiteralLineAndExitOne(t *testing.T) {
	got := run("--force", "--force-no", "backup")
	if got.code != ExitFailure {
		t.Errorf("exit = %d, want %d", got.code, ExitFailure)
	}
	if got.errText() != ForceConflictMessage+"\n" {
		t.Errorf("stderr = %q, want %q once its colour is stripped", got.stderr, ForceConflictMessage+"\n")
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want empty", got.stdout)
	}
}

func TestConflictingForceFlagsAreRejectedForEverySubcommand(t *testing.T) {
	for _, argv := range [][]string{
		{"-f", "--force-no", "list"},
		{"-f", "--force-no", "show", "vim"},
		{"-f", "--force-no", "restore"},
		{"-f", "--force-no", "link", "install"},
		{"-f", "--force-no"},
	} {
		if got := run(argv...); got.code != ExitFailure || got.errText() != ForceConflictMessage+"\n" {
			t.Errorf("mackup %s = %+v, want the exclusion line and exit 1", argv, got)
		}
	}
}

func TestUnrecognizedSubcommandWarnsThenPrintsUsageOnStderr(t *testing.T) {
	got := run("frobnicate")
	if got.code != ExitFailure {
		t.Errorf("exit = %d, want %d", got.code, ExitFailure)
	}
	if !strings.Contains(got.errText(), "frobnicate") {
		t.Errorf("stderr = %q, want it to name the unmatched argument", got.stderr)
	}
	if !strings.Contains(got.errText(), "Usage:") {
		t.Errorf("stderr = %q, want the usage block after the warning", got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want empty on a usage error", got.stdout)
	}
	if warn, _, _ := strings.Cut(got.errText(), "\n"); !strings.Contains(warn, "frobnicate") {
		t.Errorf("first stderr line = %q, want the warning before the usage block", warn)
	}
}

func TestShowWithoutApplicationIsAUsageError(t *testing.T) {
	got := run("show")
	if got.code != ExitFailure || !strings.Contains(got.errText(), "Usage:") {
		t.Errorf("mackup show = %+v, want a usage error", got)
	}
}

func TestBareInvocationShowsUsage(t *testing.T) {
	// appspec/02 "Argument-parser behavior": a bare invocation is a usage
	// display, observed exit 0, and never reaches the config gate.
	got := run()
	if got.code != ExitOK {
		t.Errorf("exit = %d, want %d", got.code, ExitOK)
	}
	if !strings.Contains(got.stdout, "Usage:") {
		t.Errorf("stdout = %q, want the usage block", got.stdout)
	}
}

func TestSubcommandsReachDispatch(t *testing.T) {
	// Until the last link ticket lands, `link uninstall` reports that it is
	// unimplemented -- but it does so from dispatch, which proves argv carried
	// it through the whole pipeline. list, show, backup, restore, link install
	// and link are no longer in this list because they now do their work; the
	// cases elsewhere assert that, which is the same claim in its final form.
	for _, argv := range [][]string{
		{"link", "uninstall"},
		{"link", "uninstall", "vim"},
	} {
		got := run(argv...)
		if got.code == ExitOK {
			t.Errorf("mackup %s exit = 0, want non-zero while unimplemented", argv)
		}
		if !strings.Contains(got.errText(), "not implemented") {
			t.Errorf("mackup %s stderr = %q, want the unimplemented diagnostic", argv, got.stderr)
		}
		if got.stdout != "" {
			t.Errorf("mackup %s stdout = %q, want empty", argv, got.stdout)
		}
	}
}

// failingWriter fails every write, the way a write to a full disk does.
type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestUndeliveredOutputIsNotReportedAsSuccess(t *testing.T) {
	// Go raises SIGPIPE for a closed pipe on fd 1/2, but a redirect to a full
	// disk just fails the write. A run whose output never reached the user has
	// not completed the requested action and must not exit 0.
	for _, argv := range [][]string{{"--help"}, {"--version"}, {}} {
		var errOut bytes.Buffer
		streams := &ui.IO{
			In:  strings.NewReader(""),
			Out: failingWriter{errors.New("no space left on device")},
			Err: &errOut,
		}
		if code := Main(argv, streams); code != ExitFailure {
			t.Errorf("mackup %s with a failing stdout = %d, want %d", argv, code, ExitFailure)
		}
		if !strings.Contains(ui.StripColor(errOut.String()), "no space left on device") {
			t.Errorf("mackup %s stderr = %q, want the write failure named", argv, errOut.String())
		}
	}
}

func TestAFailedWriteDoesNotMaskAnExistingFailureCode(t *testing.T) {
	streams := &ui.IO{
		In:  strings.NewReader(""),
		Out: io.Discard,
		Err: failingWriter{errors.New("stderr is gone")},
	}
	if code := Main([]string{"--force", "--force-no", "backup"}, streams); code != ExitFailure {
		t.Errorf("exit = %d, want %d", code, ExitFailure)
	}
}

// TestTheVersionBannerIsColoredOffATerminal is half of this ticket's
// done-claim: --version still shows SGR when nothing is a terminal. The
// streams here are bytes.Buffers, which is as far from a TTY as a stream gets;
// the conformance suite makes the same assertion against a real process
// writing down a pipe.
//
// appspec/07: "The program does not condition color on whether stdout is a
// TTY." A program that consulted a terminal would pass every other case in
// this file and fail only this one.
func TestTheVersionBannerIsColoredOffATerminal(t *testing.T) {
	got := run("--version")
	if !ui.HasColor(got.stdout) {
		t.Errorf("stdout = %q, want the banner coloured even though the stream is not a terminal", got.stdout)
	}
	if want := ui.Colorize(ui.Progress, strings.TrimSuffix(got.outText(), "\n")) + "\n"; got.stdout != want {
		t.Errorf("stdout = %q, want %q: appspec/07 gives normal info yellow", got.stdout, want)
	}
}

// TestFatalDiagnosticsAreColoredOnStderr is the other half: a fatal error path
// shows SGR, in the bright red appspec/07 assigns errors that exit, on stderr.
//
// Every fatal path the program has today, not one of them: appspec/02's
// exit-code table calls these "a single colored diagnostic line", and a level
// applied at one call site and forgotten at the next is exactly the drift the
// ui.Level type exists to prevent. Each entry names an argv that reaches a
// different Sayf in the program.
func TestFatalDiagnosticsAreColoredOnStderr(t *testing.T) {
	for _, c := range []struct {
		what string
		argv []string
	}{
		{"the force-flag conflict", []string{"--force", "--force-no", "backup"}},
		{"a usage error's warning line", []string{"frobnicate"}},
		{"an unimplemented subcommand", []string{"link"}},
		{"an unknown application named to show", []string{"show", "frobnicate"}},
		{"the environment gate's storage-root refusal", []string{"-c", absentStorageConfig, "list"}},
	} {
		got := run(c.argv...)
		if got.code == ExitOK {
			t.Errorf("%s: mackup %s exited 0, so it is not a fatal path and this case proves nothing", c.what, c.argv)
			continue
		}
		if !ui.HasColor(got.stderr) {
			t.Errorf("%s: stderr = %q, want a coloured diagnostic", c.what, got.stderr)
			continue
		}
		if !strings.HasPrefix(got.stderr, "\x1b[91m") {
			t.Errorf("%s: stderr = %q, want it to open in bright red (91), the colour appspec/07 gives errors that exit", c.what, got.stderr)
		}
		if got.stdout != "" {
			t.Errorf("%s: stdout = %q, want nothing: a fatal path writes no stdout", c.what, got.stdout)
		}
	}
}

// TestTheUsageBlockIsNotColored records a decision rather than discovering
// one, so the next reader does not "fix" it.
//
// appspec/07's colour scheme lists a level for progress, anomalies, success,
// errors, verbose traces and diff decoration -- and none for the argument
// parser's own usage text, which appspec/02 separately declares human-facing
// wording rather than a machine-read contract. So the block is ROUTED by this
// ticket (stdout for --help and a bare invocation, stderr after a usage-error
// diagnostic) and left uncoloured; colouring it would mean inventing a level
// the specification does not have.
//
// The warning line that precedes it on a usage error is a different message
// and IS coloured, which TestFatalDiagnosticsAreColoredOnStderr pins.
func TestTheUsageBlockIsNotColored(t *testing.T) {
	for _, c := range []struct {
		what   string
		argv   []string
		stream func(result) string
	}{
		{"--help", []string{"--help"}, func(r result) string { return r.stdout }},
		{"a bare invocation", nil, func(r result) string { return r.stdout }},
	} {
		if text := c.stream(run(c.argv...)); ui.HasColor(text) {
			t.Errorf("%s: %q carries colour; the usage block has no level in appspec/07", c.what, text)
		}
	}
	// On a usage error stderr carries the coloured warning AND the uncoloured
	// block, so the assertion is about the block alone: everything after the
	// first line.
	got := run("frobnicate")
	_, block, found := strings.Cut(got.stderr, "\n")
	if !found {
		t.Fatalf("stderr = %q, want a warning line and then the usage block", got.stderr)
	}
	if ui.HasColor(block) {
		t.Errorf("the usage block on stderr = %q, want it uncoloured", block)
	}
}

// TestTheRootGuardRefusesTheSuperuserWithoutRoot drives both arms of
// appspec/07's "The superuser (root) guard", which is the one contract in this
// program no test can reach by running as the user it runs as.
//
// appspec/07 marks it UNVERIFIED for exactly that reason -- "the harness ran
// only as a non-root user, so the root-refusal path and the --root bypass were
// not exercised directly" -- so the conformance suite can observe the
// permitted arm and nothing else. Without the effectiveUID seam the refusing
// arm is a branch no fixture takes, which is to say a branch a green gate says
// nothing about: deleting the guard entirely would pass every other case in
// this repository.
func TestTheRootGuardRefusesTheSuperuserWithoutRoot(t *testing.T) {
	defer asSuperuser()()

	got := run("list")
	if got.code != ExitFailure {
		t.Errorf("exit = %d, want %d", got.code, ExitFailure)
	}
	if !strings.Contains(got.errText(), "superuser") {
		t.Errorf("stderr = %q, want the superuser warning appspec/07 specifies", got.stderr)
	}
	if !strings.Contains(got.errText(), "--help") {
		t.Errorf("stderr = %q, want it to point at `mackup --help` for guidance", got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want nothing: the guard aborts before the command does work", got.stdout)
	}
}

// TestTheRootFlagPermitsTheSuperuser is the bypass half: "With --root / -r,
// running as superuser is permitted (the guard passes)." Both spellings,
// because appspec/02 makes the short and long forms interchangeable and a
// guard reading only one of them refuses a user who typed the other.
func TestTheRootFlagPermitsTheSuperuser(t *testing.T) {
	defer asSuperuser()()

	for _, argv := range [][]string{{"--root", "list"}, {"-r", "list"}} {
		got := run(argv...)
		if got.code != ExitOK {
			t.Errorf("mackup %s exit = %d, want %d: --root permits the superuser", argv, got.code, ExitOK)
		}
		if !strings.HasPrefix(got.outText(), listHeader) {
			t.Errorf("mackup %s stdout = %q, want the listing", argv, got.stdout)
		}
	}
}

// TestTheRootGuardRefusesEveryCommandAlike pins the "universal" in appspec/01
// section 4's universal environment check: the guard is level 1 of the
// lattice, which every command passes identically, so list and show are
// refused in the same words as a sync command.
func TestTheRootGuardRefusesEveryCommandAlike(t *testing.T) {
	defer asSuperuser()()

	first := run("list")
	for _, argv := range [][]string{{"show", "vim"}, {"backup"}, {"restore", "vim"}, {"link"}} {
		got := run(argv...)
		if got.code != ExitFailure || got.stderr != first.stderr {
			t.Errorf("mackup %s = %+v, want the same refusal `mackup list` got: %q", argv, got, first.stderr)
		}
	}
}

// TestTheRootGuardIsCheckedBeforeTheStorageRoot states the order of the two
// level-1 checks, which is only observable when both would fail.
//
// appspec/01 section 3 says effective UID 0 "aborts any command before it does
// work", and stating which diagnostic wins is what keeps that from being
// re-ordered later on the grounds that the storage root is the cheaper check.
func TestTheRootGuardIsCheckedBeforeTheStorageRoot(t *testing.T) {
	defer asSuperuser()()

	text := run("-c", absentStorageConfig, "list").errText()
	if !strings.Contains(text, "superuser") {
		t.Errorf("stderr = %q, want the root guard's refusal", text)
	}
	if strings.Contains(text, "Unable to find the storage folder") {
		t.Errorf("stderr = %q, want the storage root not to have been consulted at all", text)
	}
}

// asSuperuser makes the guard see effective UID 0 and returns the restore.
//
// A function rather than a bare assignment so the restore cannot be forgotten:
// a case that left the override in place would make every case after it in
// this package run as a superuser, and the ones that expect a command to
// succeed would fail somewhere other than where the defect is.
func asSuperuser() func() {
	restore := effectiveUID
	effectiveUID = func() int { return 0 }
	return func() { effectiveUID = restore }
}

// TestAnAbsentStorageRootIsRefusedByTheGateAndNamed is appspec/07's
// "Storage-root directory missing (usable-env check)" row: the guarded
// `Error: Unable to find the storage folder: <path>` line, exit 1.
//
// It is also where appspec/04's deliberately missing existence check lands.
// The config here RESOLVES -- the file_system engine returns its path without
// looking -- so a run that got this far proves the engine did not check, and a
// run that stops here proves the gate did.
func TestAnAbsentStorageRootIsRefusedByTheGateAndNamed(t *testing.T) {
	got := run("-c", absentStorageConfig, "list")
	if got.code != ExitFailure {
		t.Errorf("exit = %d, want %d", got.code, ExitFailure)
	}
	want := "Error: Unable to find the storage folder: " + filepath.Join(os.Getenv("HOME"), absentStorageDirectory) + "\n"
	if got.errText() != want {
		t.Errorf("stderr = %q, want exactly %q once its colour is stripped", got.stderr, want)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want nothing", got.stdout)
	}
}

// TestTheStorageRootMustBeADirectory reads appspec/01 section 4's wording as
// it is written -- "storage-root DIRECTORY exists" -- rather than as "the path
// is there".
//
// A regular file at the storage root cannot hold the Mackup folder appspec/06
// creates inside it, so accepting one only moves the failure to the first copy,
// where appspec/07 has no row for it.
func TestTheStorageRootMustBeADirectory(t *testing.T) {
	home := os.Getenv("HOME")
	name := "storage-is-a-file.cfg"
	root := filepath.Join(home, "storage-file")
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	defer os.Remove(root)
	config := "[storage]\nengine = file_system\npath = storage-file\n"
	if err := os.WriteFile(filepath.Join(home, name), []byte(config), 0o600); err != nil {
		t.Fatalf("writing the config: %v", err)
	}
	defer os.Remove(filepath.Join(home, name))

	got := run("-c", name, "list")
	if got.code != ExitFailure || !strings.Contains(got.errText(), "Unable to find the storage folder: "+root) {
		t.Errorf("mackup -c %s list = %+v, want the storage folder refused and named", name, got)
	}
}

// TestTheEnvironmentGateBlocksListAndShowLikeEverythingElse is the done-claim
// of this ticket read literally: appspec/01 section 1 says list and show
// "otherwise touch no storage", and appspec/02 says they fail anyway.
//
// Compared against the sync commands' own output rather than against a
// sentence written here, so the claim is "identically" and not merely "also".
func TestTheEnvironmentGateBlocksListAndShowLikeEverythingElse(t *testing.T) {
	backup := run("-c", absentStorageConfig, "backup")
	for _, argv := range [][]string{{"list"}, {"show", "vim"}} {
		got := run(append([]string{"-c", absentStorageConfig}, argv...)...)
		if got.code != backup.code || got.stderr != backup.stderr || got.stdout != "" {
			t.Errorf("mackup %s = %+v, want exactly what backup got: exit %d, stderr %q", argv, got, backup.code, backup.stderr)
		}
	}
}

// listing is `list` output split into the three parts appspec/05 gives it: the
// header, the key lines, and the count trailer.
type listing struct {
	keys    []string
	trailer string
}

// parseListing reads `list` output as appspec/05 "Enumeration" describes it,
// failing the case if the shape is not the one specified.
//
// The shape is checked here rather than assumed, so that a case asserting
// something ABOUT the keys cannot pass over output that is not a listing at
// all -- which is what an assertion built from Contains on a 617-line stream
// would do.
func parseListing(t *testing.T, got result) listing {
	t.Helper()
	if got.code != ExitOK {
		t.Fatalf("mackup list exit = %d, want %d\nstderr: %q", got.code, ExitOK, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want nothing: appspec/07 puts list output on stdout", got.stderr)
	}
	lines := strings.Split(strings.TrimSuffix(got.outText(), "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("stdout = %q, want a header, key lines, a blank line and a trailer", got.stdout)
	}
	if lines[0] != listHeader {
		t.Fatalf("first line = %q, want %q", lines[0], listHeader)
	}
	if blank := lines[len(lines)-2]; blank != "" {
		t.Errorf("the line before the trailer = %q, want it blank as appspec/05 writes the block", blank)
	}
	var keys []string
	for _, line := range lines[1 : len(lines)-2] {
		if !strings.HasPrefix(line, entryPrefix) {
			t.Fatalf("key line = %q, want it to open with %q", line, entryPrefix)
		}
		keys = append(keys, strings.TrimPrefix(line, entryPrefix))
	}
	return listing{keys: keys, trailer: lines[len(lines)-1]}
}

// TestListPrintsSortedKeysAndACountThatMatchesThem pins the two halves of
// appspec/05's list block against each other.
//
// The count is not asserted as a number: 614 is the catalog's business and
// internal/catalog pins it against the appendix. What this case asserts is
// that the trailer counts the lines actually printed -- the property
// appspec/05 states as one claim ("makes key myapp appear in list, increments
// the supported-count trailer by one") and that two independently computed
// numbers would break.
func TestListPrintsSortedKeysAndACountThatMatchesThem(t *testing.T) {
	got := parseListing(t, run("list"))

	if len(got.keys) == 0 {
		t.Fatal("list printed no applications; the built-in catalog should be in the binary")
	}
	if !sort.StringsAreSorted(got.keys) {
		t.Error("the keys are not sorted ascending, which appspec/05 requires of list output")
	}
	want := fmt.Sprintf("%d applications supported in Mackup v%s", len(got.keys), version.String())
	if got.trailer != want {
		t.Errorf("trailer = %q, want %q", got.trailer, want)
	}
}

// TestTheListTrailerReportsTheSameVersionAsTheBanner keeps the trailer's
// version from becoming a literal.
//
// appspec/05 gives the trailer as "Mackup v<version>" and appspec/00 makes the
// version itself a contract with a fallback token -- so a trailer hardcoding
// the reference build's 0.11.1 would satisfy the format and report a version
// this build is not.
func TestTheListTrailerReportsTheSameVersionAsTheBanner(t *testing.T) {
	trailer := parseListing(t, run("list")).trailer
	banner := strings.TrimSuffix(run("--version").outText(), "\n")

	if want := "Mackup v" + strings.TrimPrefix(banner, "Mackup "); !strings.HasSuffix(trailer, want) {
		t.Errorf("trailer = %q, want it to end in %q, the version --version reports", trailer, want)
	}
}

// TestListIsNotNarrowedByTheConfigApplicationLists is a contract stated in one
// sentence of appspec/03 and easy to lose: of the allowlist, "this section
// does NOT affect list output".
//
// It is what makes list an audit surface (appspec/00 promise 5, "let the user
// see the whole catalog"): a user who has narrowed their sync scope still
// needs to see everything the narrowing was drawn from. A reimplementation
// that ran the config's lists over the keys -- which is exactly what the
// selector of appspec/01 section 1 does for the SYNC commands -- would print a
// shorter listing and a smaller count, and no other case here would notice.
func TestListIsNotNarrowedByTheConfigApplicationLists(t *testing.T) {
	all := parseListing(t, run("list")).keys
	if len(all) < 2 {
		t.Fatalf("the catalog holds %d applications; this case needs two to name", len(all))
	}
	home := os.Getenv("HOME")
	name := "scoped.cfg"
	config := "[storage]\nengine = file_system\npath = storage\n" +
		"\n[applications_to_sync]\n" + all[0] + "\n" +
		"\n[applications_to_ignore]\n" + all[1] + "\n"
	if err := os.WriteFile(filepath.Join(home, name), []byte(config), 0o600); err != nil {
		t.Fatalf("writing the config: %v", err)
	}
	defer os.Remove(filepath.Join(home, name))

	scoped := parseListing(t, run("-c", name, "list"))
	if !reflect.DeepEqual(scoped.keys, all) {
		t.Errorf("list under an allowlist and a denylist printed %d applications, want the same %d it prints without one", len(scoped.keys), len(all))
	}
	if want := fmt.Sprintf("%d applications", len(all)); !strings.HasPrefix(scoped.trailer, want) {
		t.Errorf("trailer = %q, want it to open with %q: the count follows the keys printed", scoped.trailer, want)
	}
}

// TestShowPrintsTheDisplayNameAndTheSortedFileSet is appspec/05's show block.
//
// `mackup` is the application named because it is the one definition whose
// content the specification itself fixes: appspec/03 makes ~/.mackup.cfg and
// ~/.mackup the user's own configuration, and appspec/06's whole-Mackup mode
// syncs them, so the key is not free to drift the way an ordinary
// application's authored file set is.
func TestShowPrintsTheDisplayNameAndTheSortedFileSet(t *testing.T) {
	got := run("show", "mackup")
	if got.code != ExitOK {
		t.Fatalf("exit = %d, want %d\nstderr: %q", got.code, ExitOK, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want nothing: appspec/07 puts show output on stdout", got.stderr)
	}
	lines := strings.Split(strings.TrimSuffix(got.outText(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("stdout = %q, want a name line and the file header", got.stdout)
	}
	if !strings.HasPrefix(lines[0], showNamePrefix) || strings.TrimPrefix(lines[0], showNamePrefix) == "" {
		t.Errorf("first line = %q, want %q followed by a display name", lines[0], showNamePrefix)
	}
	if lines[1] != showFilesHeader {
		t.Errorf("second line = %q, want %q", lines[1], showFilesHeader)
	}
	var paths []string
	for _, line := range lines[2:] {
		if !strings.HasPrefix(line, entryPrefix) {
			t.Fatalf("path line = %q, want it to open with %q", line, entryPrefix)
		}
		path := strings.TrimPrefix(line, entryPrefix)
		if strings.HasPrefix(path, "/") {
			t.Errorf("path = %q, want a home-relative one: appspec/05 rejects absolute paths at assembly", path)
		}
		paths = append(paths, path)
	}
	if !sort.StringsAreSorted(paths) {
		t.Errorf("paths = %v, want them sorted ascending", paths)
	}
	if want := []string{".mackup", ".mackup.cfg"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("show mackup listed %v, want %v -- the user's own config and custom-apps directory", paths, want)
	}
}

// TestShowRefusesAnUnknownApplicationWithTheLiteralToken is appspec/07's
// `Unsupported application: <name>` row, one of the three literal tokens that
// file calls contract "matched by scripts/tests".
//
// The whole line is asserted, not a substring of it: a token a script greps
// for is only as good as its exact spelling, and an assertion by Contains
// passes over a program that has wrapped the token in something else.
func TestShowRefusesAnUnknownApplicationWithTheLiteralToken(t *testing.T) {
	got := run("show", "frobnicate")
	if got.code != ExitFailure {
		t.Errorf("exit = %d, want %d", got.code, ExitFailure)
	}
	if want := UnsupportedApplicationPrefix + "frobnicate\n"; got.errText() != want {
		t.Errorf("stderr = %q, want exactly %q once its colour is stripped", got.stderr, want)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want nothing: the command refused its argument", got.stdout)
	}
}

// TestShowIsCaseSensitiveAboutTheKey follows from appspec/05 making the
// definition's filename the key and appspec/03 lowercasing only the CONFIG's
// application lists. The keys the catalog ships are lowercase, so a user
// typing "Vim" has named an application that does not exist.
func TestShowIsCaseSensitiveAboutTheKey(t *testing.T) {
	if got := run("show", "Vim"); got.code != ExitFailure ||
		got.errText() != UnsupportedApplicationPrefix+"Vim\n" {
		t.Errorf("mackup show Vim = %+v, want it refused as an unknown key", got)
	}
}

// TestListAndShowPrintTheirOutputInColorOffATerminal extends appspec/07's
// unconditional-colour rule to the two commands this ticket adds. They are the
// first commands whose SUCCESS writes to stdout, so before them the rule was
// observable only on the --version banner.
func TestListAndShowPrintTheirOutputInColorOffATerminal(t *testing.T) {
	for _, argv := range [][]string{{"list"}, {"show", "mackup"}} {
		got := run(argv...)
		if !ui.HasColor(got.stdout) {
			t.Errorf("mackup %s stdout = %q, want it coloured even though the stream is not a terminal", argv, got.stdout)
		}
		for _, line := range strings.Split(strings.TrimSuffix(got.stdout, "\n"), "\n") {
			if !strings.HasPrefix(line, "\x1b[33m") || !strings.HasSuffix(line, "\x1b[0m") {
				t.Errorf("mackup %s line = %q, want it opened in yellow (33) and terminated with a reset", argv, line)
			}
		}
	}
}
