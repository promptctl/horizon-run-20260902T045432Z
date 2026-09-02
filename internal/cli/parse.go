// Package cli implements the argv boundary specified by
// appspec/02-invocation.md: the invocation grammar, the global-options table,
// and the usage errors the parser reports for argv that matches no usage line.
package cli

import (
	"fmt"
	"strings"
)

// Command identifies which usage line argv matched.
type Command int

const (
	// CmdNone is a bare invocation with no subcommand. appspec/02
	// "Argument-parser behavior" treats it as a usage display, not an error.
	CmdNone Command = iota
	CmdList
	CmdShow
	CmdBackup
	CmdRestore
	CmdLinkInstall
	CmdLinkUninstall
	CmdLink
)

// String returns the subcommand as it is written on the command line.
func (c Command) String() string {
	switch c {
	case CmdList:
		return "list"
	case CmdShow:
		return "show"
	case CmdBackup:
		return "backup"
	case CmdRestore:
		return "restore"
	case CmdLinkInstall:
		return "link install"
	case CmdLinkUninstall:
		return "link uninstall"
	case CmdLink:
		return "link"
	default:
		return ""
	}
}

// Options is the global-options table of appspec/02-invocation.md. Every
// option may appear before or after the subcommand; short and long forms are
// interchangeable.
type Options struct {
	Help    bool
	Version bool
	Force   bool
	ForceNo bool
	Root    bool
	DryRun  bool
	Verbose bool

	// ConfigFile is the -c/--config-file argument. ConfigFileSet distinguishes
	// "not given" from "given as the empty string"; resolution of the path
	// itself belongs to the config resolver (appspec/03).
	ConfigFile    string
	ConfigFileSet bool
}

// Invocation is a fully parsed command line.
type Invocation struct {
	Opts Options
	Cmd  Command

	// Application is the <application> key when one was named, empty otherwise.
	// It is a key such as "vim", never a display name.
	Application string
}

// UsageError reports argv that matches none of the usage lines. Warning names
// the offending argument; callers print it ahead of the usage block.
type UsageError struct {
	Warning string
}

func (e *UsageError) Error() string { return e.Warning }

func usageErrf(format string, args ...any) error {
	return &UsageError{Warning: fmt.Sprintf(format, args...)}
}

// longOpts maps a long option name to whether it takes an argument.
var longOpts = map[string]bool{
	"help":        false,
	"version":     false,
	"force":       false,
	"force-no":    false,
	"root":        false,
	"dry-run":     false,
	"verbose":     false,
	"config-file": true,
}

// shortOpts maps a short option letter to its long name.
var shortOpts = map[byte]string{
	'h': "help",
	'f': "force",
	'r': "root",
	'n': "dry-run",
	'v': "verbose",
	'c': "config-file",
}

// Parse turns argv (without the program name) into an Invocation. It reports a
// *UsageError for argv matching no usage line; every other outcome is a
// well-formed invocation, including the bare CmdNone one.
//
// --help and --version stop the scan where they are found, so nothing after
// them is examined: appspec/02-invocation.md gives each its own usage line and
// says it takes no other action. Stopping mid-scan rather than pre-scanning
// argv keeps "mackup -c --help list" reading --help as the config-file
// argument it is written as.
func Parse(argv []string) (Invocation, error) {
	var inv Invocation
	var positional []string

	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "-" || !strings.HasPrefix(arg, "-"):
			positional = append(positional, arg)
		case strings.HasPrefix(arg, "--"):
			name, value, hasValue := strings.Cut(arg[2:], "=")
			takesArg, known := longOpts[name]
			if !known {
				return inv, usageErrf("unrecognized option: --%s", name)
			}
			if !takesArg && hasValue {
				return inv, usageErrf("option --%s does not take an argument", name)
			}
			if takesArg && !hasValue {
				i++
				if i >= len(argv) {
					return inv, usageErrf("option --%s requires an argument", name)
				}
				value = argv[i]
			}
			if err := inv.Opts.set(name, value); err != nil {
				return inv, err
			}
			if inv.Opts.Help || inv.Opts.Version {
				return inv, nil
			}
		default:
			// A short-option cluster: every letter is a flag except a final
			// letter that takes an argument, which consumes the rest of the
			// cluster or the next argv element.
			cluster := arg[1:]
			for j := 0; j < len(cluster); j++ {
				name, known := shortOpts[cluster[j]]
				if !known {
					return inv, usageErrf("unrecognized option: -%c", cluster[j])
				}
				if !longOpts[name] {
					if err := inv.Opts.set(name, ""); err != nil {
						return inv, err
					}
					if inv.Opts.Help || inv.Opts.Version {
						return inv, nil
					}
					continue
				}
				value := strings.TrimPrefix(cluster[j+1:], "=")
				if cluster[j+1:] == "" {
					i++
					if i >= len(argv) {
						return inv, usageErrf("option -%c requires an argument", cluster[j])
					}
					value = argv[i]
				}
				if err := inv.Opts.set(name, value); err != nil {
					return inv, err
				}
				j = len(cluster)
			}
		}
	}

	cmd, app, err := matchCommand(positional)
	if err != nil {
		return inv, err
	}
	inv.Cmd = cmd
	inv.Application = app
	return inv, nil
}

func (o *Options) set(name, value string) error {
	switch name {
	case "help":
		o.Help = true
	case "version":
		o.Version = true
	case "force":
		o.Force = true
	case "force-no":
		o.ForceNo = true
	case "root":
		o.Root = true
	case "dry-run":
		o.DryRun = true
	case "verbose":
		o.Verbose = true
	case "config-file":
		o.ConfigFile = value
		o.ConfigFileSet = true
	default:
		return usageErrf("unrecognized option: --%s", name)
	}
	return nil
}

// matchCommand matches the positional arguments against the usage lines of
// appspec/02-invocation.md, in the order they are listed there: "link install"
// and "link uninstall" are matched before plain "link", so those two words are
// mode keywords rather than application keys.
func matchCommand(pos []string) (Command, string, error) {
	if len(pos) == 0 {
		return CmdNone, "", nil
	}

	optionalApp := func(cmd Command, rest []string) (Command, string, error) {
		switch len(rest) {
		case 0:
			return cmd, "", nil
		case 1:
			return cmd, rest[0], nil
		default:
			return CmdNone, "", usageErrf("unrecognized argument: %s", rest[1])
		}
	}

	switch pos[0] {
	case "list":
		if len(pos) > 1 {
			return CmdNone, "", usageErrf("unrecognized argument: %s", pos[1])
		}
		return CmdList, "", nil
	case "show":
		switch len(pos) {
		case 1:
			return CmdNone, "", usageErrf("show requires an <application>")
		case 2:
			return CmdShow, pos[1], nil
		default:
			return CmdNone, "", usageErrf("unrecognized argument: %s", pos[2])
		}
	case "backup":
		return optionalApp(CmdBackup, pos[1:])
	case "restore":
		return optionalApp(CmdRestore, pos[1:])
	case "link":
		if len(pos) > 1 {
			switch pos[1] {
			case "install":
				return optionalApp(CmdLinkInstall, pos[2:])
			case "uninstall":
				return optionalApp(CmdLinkUninstall, pos[2:])
			}
		}
		return optionalApp(CmdLink, pos[1:])
	default:
		return CmdNone, "", usageErrf("unrecognized argument: %s", pos[0])
	}
}
