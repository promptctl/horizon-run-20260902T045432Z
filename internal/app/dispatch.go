package app

import (
	"github.com/promptctl/macklebox/internal/appdb"
	"github.com/promptctl/macklebox/internal/cli"
	"github.com/promptctl/macklebox/internal/ui"
)

// dispatch runs the subcommand argv selected. The two enumeration commands of
// appspec/05 are implemented; each remaining arm is filled in by the ticket
// named beside it, and until then reports that it is not implemented on stderr
// and exits non-zero, so no caller can mistake an unimplemented verb for a
// completed action.
//
// The assembled application database is passed in rather than built here: every
// command reads the same one, and appspec/01 section 4 assembles it before
// dispatch so that a refused definition aborts the run whatever the subcommand
// was. The list and show arms are its first readers; every sync arm will read
// the same value rather than assembling a second.
func dispatch(inv cli.Invocation, streams *ui.IO, apps *appdb.Database) int {
	switch inv.Cmd {
	case cli.CmdList:
		return list(streams, apps)
	case cli.CmdShow:
		// The key is validated inside show, which is to say AFTER the
		// environment gate this dispatch already ran. That is where
		// appspec/02 puts it: its "validated before the environment check"
		// rule names backup, restore, link, link install and link uninstall
		// -- the commands whose gate creates a folder or shows a prompt --
		// and show is not one of them, because there is nothing for an
		// unknown key to fail cleanly ahead of.
		return show(streams, apps, inv.Application)
	case cli.CmdBackup, cli.CmdRestore:
		// TODO(macklebox-copy-sync-dpz.3): the one copy operation, run in
		// either direction (appspec/01 section 1).
		return notImplemented(inv, streams)
	case cli.CmdLinkInstall:
		// TODO(macklebox-link-sync-83q.2): move home files into storage,
		// symlink them back.
		return notImplemented(inv, streams)
	case cli.CmdLink:
		// TODO(macklebox-link-sync-83q.3): symlink files already in storage
		// into home, moving nothing out of home.
		return notImplemented(inv, streams)
	case cli.CmdLinkUninstall:
		// TODO(macklebox-link-sync-83q.4): revert links to real files, and
		// refuse to clobber a file the user substituted.
		return notImplemented(inv, streams)
	default:
		streams.Sayf(ui.Fatal, "mackup: unhandled command: %v", inv.Cmd)
		return ExitFailure
	}
}

func notImplemented(inv cli.Invocation, streams *ui.IO) int {
	streams.Sayf(ui.Fatal, "Error: %s is not implemented yet.", inv.Cmd)
	return ExitFailure
}
