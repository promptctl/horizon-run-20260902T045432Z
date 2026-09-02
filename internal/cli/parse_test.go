package cli

import (
	"errors"
	"strings"
	"testing"
)

func TestParseAcceptsEveryInvocationForm(t *testing.T) {
	// The invocation forms listed in appspec/02-invocation.md, and only these,
	// are accepted.
	tests := []struct {
		argv []string
		cmd  Command
		app  string
	}{
		{[]string{}, CmdNone, ""},
		{[]string{"list"}, CmdList, ""},
		{[]string{"show", "vim"}, CmdShow, "vim"},
		{[]string{"backup"}, CmdBackup, ""},
		{[]string{"backup", "vim"}, CmdBackup, "vim"},
		{[]string{"restore"}, CmdRestore, ""},
		{[]string{"restore", "sublime-text-3"}, CmdRestore, "sublime-text-3"},
		{[]string{"link"}, CmdLink, ""},
		{[]string{"link", "vim"}, CmdLink, "vim"},
		{[]string{"link", "install"}, CmdLinkInstall, ""},
		{[]string{"link", "install", "vim"}, CmdLinkInstall, "vim"},
		{[]string{"link", "uninstall"}, CmdLinkUninstall, ""},
		{[]string{"link", "uninstall", "vim"}, CmdLinkUninstall, "vim"},
	}
	for _, tc := range tests {
		inv, err := Parse(tc.argv)
		if err != nil {
			t.Errorf("Parse(%q) = error %v, want a match", tc.argv, err)
			continue
		}
		if inv.Cmd != tc.cmd || inv.Application != tc.app {
			t.Errorf("Parse(%q) = (%v, %q), want (%v, %q)", tc.argv, inv.Cmd, inv.Application, tc.cmd, tc.app)
		}
	}
}

func TestParseOptionsBeforeAndAfterSubcommand(t *testing.T) {
	// appspec/02: options may appear before the subcommand, and short and long
	// forms are interchangeable.
	for _, argv := range [][]string{
		{"-f", "-n", "-v", "-r", "backup", "vim"},
		{"backup", "vim", "--force", "--dry-run", "--verbose", "--root"},
		{"--force", "backup", "--dry-run", "vim", "-vr"},
	} {
		inv, err := Parse(argv)
		if err != nil {
			t.Fatalf("Parse(%q) = error %v", argv, err)
		}
		opts := inv.Opts
		if !opts.Force || !opts.DryRun || !opts.Verbose || !opts.Root {
			t.Errorf("Parse(%q) options = %+v, want force/dry-run/verbose/root all set", argv, opts)
		}
		if inv.Cmd != CmdBackup || inv.Application != "vim" {
			t.Errorf("Parse(%q) = (%v, %q), want (backup, vim)", argv, inv.Cmd, inv.Application)
		}
	}
}

func TestParseConfigFileArgumentForms(t *testing.T) {
	for _, argv := range [][]string{
		{"-c", "/home/u/.other.cfg", "list"},
		{"-c=/home/u/.other.cfg", "list"},
		{"-c/home/u/.other.cfg", "list"},
		{"--config-file", "/home/u/.other.cfg", "list"},
		{"--config-file=/home/u/.other.cfg", "list"},
	} {
		inv, err := Parse(argv)
		if err != nil {
			t.Fatalf("Parse(%q) = error %v", argv, err)
		}
		if !inv.Opts.ConfigFileSet || inv.Opts.ConfigFile != "/home/u/.other.cfg" {
			t.Errorf("Parse(%q) config file = (%q, set=%v), want /home/u/.other.cfg", argv, inv.Opts.ConfigFile, inv.Opts.ConfigFileSet)
		}
	}
}

func TestParseForceFlagsAreRecordedSeparately(t *testing.T) {
	// Parsing records both; rejecting the combination is the pipeline's job so
	// that it happens in the documented order, before config load.
	inv, err := Parse([]string{"--force", "--force-no", "backup"})
	if err != nil {
		t.Fatalf("Parse = error %v", err)
	}
	if !inv.Opts.Force || !inv.Opts.ForceNo {
		t.Errorf("options = %+v, want both force flags set", inv.Opts)
	}
}

func TestParseHelpAndVersionMatchTheirOwnUsageLines(t *testing.T) {
	for _, argv := range [][]string{{"-h"}, {"--help"}} {
		inv, err := Parse(argv)
		if err != nil || !inv.Opts.Help {
			t.Errorf("Parse(%q) = (%+v, %v), want Help set", argv, inv.Opts, err)
		}
	}
	inv, err := Parse([]string{"--version"})
	if err != nil || !inv.Opts.Version {
		t.Errorf("Parse(--version) = (%+v, %v), want Version set", inv.Opts, err)
	}
}

func TestParseRejectsFormsMatchingNoUsageLine(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string // substring the warning must name
	}{
		{"unrecognized subcommand", []string{"frobnicate"}, "frobnicate"},
		{"show without application", []string{"show"}, "show"},
		{"extra positional after list", []string{"list", "vim"}, "vim"},
		{"extra positional after show", []string{"show", "vim", "git"}, "git"},
		{"extra positional after backup", []string{"backup", "vim", "git"}, "git"},
		{"extra positional after link install", []string{"link", "install", "vim", "git"}, "git"},
		{"extra positional after link", []string{"link", "vim", "git"}, "git"},
		{"unknown long option", []string{"--nope", "list"}, "--nope"},
		{"unknown short option", []string{"-z", "list"}, "-z"},
		{"flag given an argument", []string{"--force=yes", "list"}, "--force"},
		{"missing option argument", []string{"list", "--config-file"}, "--config-file"},
		{"missing short option argument", []string{"list", "-c"}, "-c"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.argv)
			var usageErr *UsageError
			if !errors.As(err, &usageErr) {
				t.Fatalf("Parse(%q) = %v, want a *UsageError", tc.argv, err)
			}
			if !strings.Contains(usageErr.Warning, tc.want) {
				t.Errorf("warning %q does not name %q", usageErr.Warning, tc.want)
			}
		})
	}
}

func TestParseLinkModeKeywordsWinOverApplicationKeys(t *testing.T) {
	// appspec/02 lists "link install" and "link uninstall" ahead of plain
	// "link", so those two words are mode keywords in that position.
	inv, err := Parse([]string{"link", "install"})
	if err != nil || inv.Cmd != CmdLinkInstall || inv.Application != "" {
		t.Fatalf("Parse(link install) = (%v, %q, %v), want (link install, \"\", nil)", inv.Cmd, inv.Application, err)
	}
}

func TestParseHelpAndVersionStopTheScan(t *testing.T) {
	// appspec/02 gives --help and --version their own usage lines and says
	// each takes no other action, so nothing after one is examined.
	for _, argv := range [][]string{
		{"--help", "--nope"},
		{"--version", "-z"},
		{"-h", "frobnicate", "extra"},
	} {
		inv, err := Parse(argv)
		if err != nil {
			t.Errorf("Parse(%q) = error %v, want the help/version path", argv, err)
			continue
		}
		if !inv.Opts.Help && !inv.Opts.Version {
			t.Errorf("Parse(%q) set neither Help nor Version", argv)
		}
	}
}

func TestParseHelpBeforeAnOptionArgumentIsStillAnArgument(t *testing.T) {
	// -c takes an argument, so "--help" here is that argument, not the flag.
	inv, err := Parse([]string{"-c", "--help", "list"})
	if err != nil {
		t.Fatalf("Parse = error %v", err)
	}
	if inv.Opts.Help {
		t.Error("Help is set, want --help consumed as the --config-file argument")
	}
	if inv.Opts.ConfigFile != "--help" || inv.Cmd != CmdList {
		t.Errorf("Parse = (config %q, cmd %v), want (--help, list)", inv.Opts.ConfigFile, inv.Cmd)
	}
}

func TestParseStillRejectsABadOptionSeenBeforeHelp(t *testing.T) {
	var usageErr *UsageError
	if _, err := Parse([]string{"--nope", "--help"}); !errors.As(err, &usageErr) {
		t.Errorf("Parse(--nope --help) = %v, want a *UsageError: --nope genuinely came first", err)
	}
}

func TestParseRejectsTheUndocumentedDoubleDash(t *testing.T) {
	// appspec/02-invocation.md never mentions "--", and no application key in
	// appspec/appendix-application-names.md begins with a dash, so there is
	// nothing for an end-of-options marker to disambiguate.
	var usageErr *UsageError
	for _, argv := range [][]string{{"--", "list"}, {"backup", "--", "vim"}} {
		if _, err := Parse(argv); !errors.As(err, &usageErr) {
			t.Errorf("Parse(%q) = %v, want a *UsageError", argv, err)
		}
	}
}

func TestParseRejectsABareDash(t *testing.T) {
	// The reasoning that removed "--" applies here too: no application key in
	// appspec/appendix-application-names.md begins with a dash, so "-" is an
	// unmatched argument rather than a key. Rejecting it at the parser gives
	// the usage block instead of a database miss much later.
	var usageErr *UsageError
	for _, argv := range [][]string{{"-"}, {"show", "-"}, {"backup", "-"}} {
		if _, err := Parse(argv); !errors.As(err, &usageErr) {
			t.Errorf("Parse(%q) = %v, want a *UsageError", argv, err)
		}
	}
}

func TestParseNamesTheCharacterTheUserTyped(t *testing.T) {
	// Options are keyed by byte, so a naive diagnostic reports the first byte
	// of a multi-byte rune -- naming a character the user never typed.
	var usageErr *UsageError
	if _, err := Parse([]string{"-é", "list"}); !errors.As(err, &usageErr) {
		t.Fatalf("Parse(-é list) = %v, want a *UsageError", err)
	}
	if !strings.Contains(usageErr.Warning, "-é") {
		t.Errorf("warning = %q, want it to name -é", usageErr.Warning)
	}
}

func TestCommandStringIsAlwaysSelfDescribing(t *testing.T) {
	// The value feeds user-facing diagnostics, so no Command may render as an
	// empty string -- including one added later without updating String().
	for _, cmd := range []Command{CmdNone, CmdList, CmdShow, CmdBackup, CmdRestore, CmdLinkInstall, CmdLinkUninstall, CmdLink, Command(99)} {
		if cmd.String() == "" {
			t.Errorf("Command(%d).String() is empty", int(cmd))
		}
	}
}
