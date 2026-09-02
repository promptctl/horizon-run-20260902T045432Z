package app

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/promptctl/macklebox/internal/ui"
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
	// Until the resolver and sync tickets land every subcommand reports that
	// it is unimplemented -- but it does so from dispatch, which proves argv
	// carried it through the whole pipeline.
	for _, argv := range [][]string{
		{"list"},
		{"show", "vim"},
		{"backup"},
		{"restore", "vim"},
		{"link"},
		{"link", "install"},
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
		{"an unimplemented subcommand", []string{"list"}},
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
