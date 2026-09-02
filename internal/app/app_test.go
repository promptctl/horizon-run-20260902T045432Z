package app

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/promptctl/macklebox/internal/ui"
)

type result struct {
	code   int
	stdout string
	stderr string
}

func run(argv ...string) result {
	var out, errb bytes.Buffer
	code := Main(argv, &ui.IO{In: strings.NewReader(""), Out: &out, Err: &errb})
	return result{code: code, stdout: out.String(), stderr: errb.String()}
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
	if !regexp.MustCompile(`^Mackup \S+\n$`).MatchString(got.stdout) {
		t.Errorf("stdout = %q, want a single \"Mackup <version>\" line", got.stdout)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty", got.stderr)
	}
}

func TestHelpAndVersionShortCircuitBeforeAnythingElse(t *testing.T) {
	// appspec/02: they are the only paths that succeed while skipping the
	// config-load gate, and they take no other action -- not even the force
	// conflict check reached below them.
	if got := run("--help", "--force", "--force-no", "backup"); got.code != ExitOK || !strings.Contains(got.stdout, "Usage:") {
		t.Errorf("help with conflicting force flags = %+v, want usage on stdout exit 0", got)
	}
	if got := run("--version", "list"); got.code != ExitOK || !strings.HasPrefix(got.stdout, "Mackup ") {
		t.Errorf("version with a subcommand = %+v, want the version line exit 0", got)
	}
}

func TestConflictingForceFlagsEmitTheLiteralLineAndExitOne(t *testing.T) {
	got := run("--force", "--force-no", "backup")
	if got.code != ExitFailure {
		t.Errorf("exit = %d, want %d", got.code, ExitFailure)
	}
	if got.stderr != ForceConflictMessage+"\n" {
		t.Errorf("stderr = %q, want %q", got.stderr, ForceConflictMessage+"\n")
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
		if got := run(argv...); got.code != ExitFailure || got.stderr != ForceConflictMessage+"\n" {
			t.Errorf("mackup %s = %+v, want the exclusion line and exit 1", argv, got)
		}
	}
}

func TestUnrecognizedSubcommandWarnsThenPrintsUsageOnStderr(t *testing.T) {
	got := run("frobnicate")
	if got.code != ExitFailure {
		t.Errorf("exit = %d, want %d", got.code, ExitFailure)
	}
	if !strings.Contains(got.stderr, "frobnicate") {
		t.Errorf("stderr = %q, want it to name the unmatched argument", got.stderr)
	}
	if !strings.Contains(got.stderr, "Usage:") {
		t.Errorf("stderr = %q, want the usage block after the warning", got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want empty on a usage error", got.stdout)
	}
	if warn, _, _ := strings.Cut(got.stderr, "\n"); !strings.Contains(warn, "frobnicate") {
		t.Errorf("first stderr line = %q, want the warning before the usage block", warn)
	}
}

func TestShowWithoutApplicationIsAUsageError(t *testing.T) {
	got := run("show")
	if got.code != ExitFailure || !strings.Contains(got.stderr, "Usage:") {
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
		if !strings.Contains(got.stderr, "not implemented") {
			t.Errorf("mackup %s stderr = %q, want the unimplemented diagnostic", argv, got.stderr)
		}
		if got.stdout != "" {
			t.Errorf("mackup %s stdout = %q, want empty", argv, got.stdout)
		}
	}
}
